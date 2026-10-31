// Package server is the shared gRPC service template: every domain service
// gets the same health checks, Prometheus metrics, OpenTelemetry tracing,
// structured logging, panic recovery, internal-method guard, etcd registration
// and graceful shutdown.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/dso202/bank-platform/pkg/authz"
	"github.com/dso202/bank-platform/pkg/config"
	"github.com/dso202/bank-platform/pkg/logx"
	"github.com/dso202/bank-platform/pkg/registry"
	"github.com/dso202/bank-platform/pkg/telemetry"
)

// Worker is a background loop that runs until ctx is cancelled.
type Worker func(ctx context.Context)

// Service describes what a domain service plugs into the template.
type Service struct {
	Config   config.Config
	Register func(s *grpc.Server)
	Workers  []Worker
}

// Init sets up logging for the named service and loads its config.
func Init(name string) config.Config {
	cfg := config.Load(name)
	logx.Setup(name)
	return cfg
}

// Run starts the service and blocks until SIGINT/SIGTERM.
func Run(svc Service) {
	cfg := svc.Config
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTracing := telemetry.Init(ctx, cfg.Service, cfg.OTLPEndpoint)
	defer shutdownTracing()

	metrics := grpcprom.NewServerMetrics(grpcprom.WithServerHandlingTimeHistogram(
		grpcprom.WithHistogramBuckets([]float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5}),
	))
	prometheus.MustRegister(metrics)

	internal := &internalGuard{}
	gs := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(
			recoverUnary,
			metrics.UnaryServerInterceptor(grpcprom.WithExemplarFromContext(exemplar)),
			logUnary,
			internal.unary,
		),
	)
	svc.Register(gs)
	internal.load(gs)

	hs := health.NewServer()
	healthpb.RegisterHealthServer(gs, hs)
	reflection.Register(gs)
	metrics.InitializeMetrics(gs)
	for name := range gs.GetServiceInfo() {
		hs.SetServingStatus(name, healthpb.HealthCheckResponse_SERVING)
	}
	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	lis, err := net.Listen("tcp", ":"+cfg.GRPCPort)
	if err != nil {
		slog.Error("listen failed", "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	metricsSrv := &http.Server{Addr: ":" + cfg.MetricsPort, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("metrics server failed", "err", err)
		}
	}()

	var wg sync.WaitGroup
	for _, w := range svc.Workers {
		wg.Add(1)
		go func(w Worker) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					slog.Error("worker panic", "panic", r, "stack", string(debug.Stack()))
				}
			}()
			w(ctx)
		}(w)
	}

	if len(cfg.EtcdEndpoints) > 0 {
		go registry.Register(ctx, cfg.EtcdEndpoints, cfg.Service, cfg.GRPCPort)
	}

	go func() {
		slog.Info("service started", "grpc_port", cfg.GRPCPort, "metrics_port", cfg.MetricsPort)
		if err := gs.Serve(lis); err != nil {
			slog.Error("grpc serve failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	hs.Shutdown()
	done := make(chan struct{})
	go func() { gs.GracefulStop(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		gs.Stop()
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = metricsSrv.Shutdown(sctx)
	wg.Wait()
}

func exemplar(ctx context.Context) prometheus.Labels {
	if sc := trace.SpanContextFromContext(ctx); sc.IsSampled() {
		return prometheus.Labels{"traceID": sc.TraceID().String()}
	}
	return nil
}

func recoverUnary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.ErrorContext(ctx, "panic in handler", "method", info.FullMethod, "panic", r, "stack", string(debug.Stack()))
			err = status.Error(codes.Internal, "internal error")
		}
	}()
	return handler(ctx, req)
}

func logUnary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	start := time.Now()
	p := authz.FromContext(ctx)
	resp, err := handler(ctx, req)
	code := status.Code(err)
	attrs := []any{
		"method", info.FullMethod,
		"code", code.String(),
		"duration_ms", time.Since(start).Milliseconds(),
		"request_id", p.RequestID,
		"user_id", p.UserID,
	}
	if p.Internal != "" {
		attrs = append(attrs, "caller", p.Internal)
	}
	switch code {
	case codes.OK:
		if info.FullMethod != healthpb.Health_Check_FullMethodName {
			slog.InfoContext(ctx, "rpc", attrs...)
		}
	case codes.Internal, codes.Unknown, codes.DataLoss:
		slog.ErrorContext(ctx, "rpc", append(attrs, "err", err.Error())...)
	default:
		slog.WarnContext(ctx, "rpc", append(attrs, "err", status.Convert(err).Message())...)
	}
	return resp, err
}

// internalGuard rejects external calls to methods that have no google.api.http
// route: those methods are reachable only by other services, never through the gateway.
type internalGuard struct {
	methods map[string]bool
}

func (g *internalGuard) load(gs *grpc.Server) {
	g.methods = map[string]bool{}
	for svcName, info := range gs.GetServiceInfo() {
		d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(svcName))
		if err != nil {
			continue
		}
		sd, ok := d.(protoreflect.ServiceDescriptor)
		if !ok {
			continue
		}
		for _, m := range info.Methods {
			md := sd.Methods().ByName(protoreflect.Name(m.Name))
			if md == nil {
				continue
			}
			if !proto.HasExtension(md.Options(), annotations.E_Http) {
				g.methods[fmt.Sprintf("/%s/%s", svcName, m.Name)] = true
			}
		}
	}
}

func (g *internalGuard) unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if g.methods[info.FullMethod] && authz.FromContext(ctx).Internal == "" {
		return nil, status.Error(codes.PermissionDenied, "internal method")
	}
	return handler(ctx, req)
}
