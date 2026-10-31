// api-gateway is the single REST entry point. It validates JWTs, enforces
// route-level RBAC and rate limits, attaches request IDs, and translates REST
// to gRPC for the 14 domain services. Services are never exposed directly.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/encoding/protojson"

	accountv1 "github.com/dso202/bank-platform/gen/go/bank/account/v1"
	auditv1 "github.com/dso202/bank-platform/gen/go/bank/audit/v1"
	authv1 "github.com/dso202/bank-platform/gen/go/bank/auth/v1"
	cardv1 "github.com/dso202/bank-platform/gen/go/bank/card/v1"
	customerv1 "github.com/dso202/bank-platform/gen/go/bank/customer/v1"
	fraudv1 "github.com/dso202/bank-platform/gen/go/bank/fraud/v1"
	kycv1 "github.com/dso202/bank-platform/gen/go/bank/kyc/v1"
	ledgerv1 "github.com/dso202/bank-platform/gen/go/bank/ledger/v1"
	loanv1 "github.com/dso202/bank-platform/gen/go/bank/loan/v1"
	notificationv1 "github.com/dso202/bank-platform/gen/go/bank/notification/v1"
	paymentv1 "github.com/dso202/bank-platform/gen/go/bank/payment/v1"
	statementv1 "github.com/dso202/bank-platform/gen/go/bank/statement/v1"
	transactionv1 "github.com/dso202/bank-platform/gen/go/bank/transaction/v1"
	transferv1 "github.com/dso202/bank-platform/gen/go/bank/transfer/v1"
	"github.com/dso202/bank-platform/pkg/clients"
	"github.com/dso202/bank-platform/pkg/config"
	"github.com/dso202/bank-platform/pkg/logx"
	"github.com/dso202/bank-platform/pkg/registry"
	"github.com/dso202/bank-platform/pkg/telemetry"
)

type registrar func(context.Context, *runtime.ServeMux, *grpc.ClientConn) error

var routes = map[string]registrar{
	"auth":         authv1.RegisterAuthServiceHandler,
	"customer":     customerv1.RegisterCustomerServiceHandler,
	"kyc":          kycv1.RegisterKycServiceHandler,
	"account":      accountv1.RegisterAccountServiceHandler,
	"ledger":       ledgerv1.RegisterLedgerServiceHandler,
	"transaction":  transactionv1.RegisterTransactionServiceHandler,
	"transfer":     transferv1.RegisterTransferServiceHandler,
	"payment":      paymentv1.RegisterPaymentServiceHandler,
	"card":         cardv1.RegisterCardServiceHandler,
	"loan":         loanv1.RegisterLoanServiceHandler,
	"fraud":        fraudv1.RegisterFraudServiceHandler,
	"audit":        auditv1.RegisterAuditServiceHandler,
	"notification": notificationv1.RegisterNotificationServiceHandler,
	"statement":    statementv1.RegisterStatementServiceHandler,
}

func main() {
	cfg := config.Load("api-gateway")
	logx.Setup(cfg.Service)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownTracing := telemetry.Init(ctx, cfg.Service, cfg.OTLPEndpoint)
	defer shutdownTracing()

	// Plain connections: the gateway must never mark itself as an internal caller,
	// so internal-only methods stay unreachable from outside.
	conns := map[string]*grpc.ClientConn{}
	for name := range routes {
		cc, err := grpc.NewClient(clients.Addr(name),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithStatsHandler(otelgrpc.NewClientHandler()))
		if err != nil {
			slog.Error("dial failed", "service", name, "err", err)
			os.Exit(1)
		}
		conns[name] = cc
	}

	mux := runtime.NewServeMux(
		runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.HTTPBodyMarshaler{Marshaler: &runtime.JSONPb{
			MarshalOptions:   protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true},
			UnmarshalOptions: protojson.UnmarshalOptions{DiscardUnknown: true},
		}}),
		runtime.WithIncomingHeaderMatcher(headerMatcher),
		runtime.WithOutgoingHeaderMatcher(func(string) (string, bool) { return "", false }),
		runtime.WithMetadata(principalMetadata),
		runtime.WithErrorHandler(errorHandler),
		runtime.WithRoutingErrorHandler(routingErrorHandler),
	)
	for name, reg := range routes {
		if err := reg(ctx, mux, conns[name]); err != nil {
			slog.Error("register routes", "service", name, "err", err)
			os.Exit(1)
		}
	}

	jwks := newJWKS(authv1.NewAuthServiceClient(conns["auth"]))
	limiter := newLimiter(ctx, cfg.EtcdEndpoints)

	root := http.NewServeMux()
	root.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	root.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		cctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if _, err := healthpb.NewHealthClient(conns["auth"]).Check(cctx, &healthpb.HealthCheckRequest{}); err != nil {
			http.Error(w, "auth-service not ready", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready"))
	})
	root.Handle("/v1/", chain(mux,
		recoverer,
		requestID,
		metricsMiddleware,
		cors(strings.Split(config.Get("CORS_ORIGINS", "http://localhost:3000"), ",")),
		limiter.middleware,
		authenticate(jwks),
		authorize,
		bodyLimit(4<<20),
	))

	srv := &http.Server{
		Addr:              ":" + config.Get("HTTP_PORT", "8080"),
		Handler:           otelhttp.NewHandler(root, "api-gateway", otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method + " " + routeLabel(r.URL.Path) })),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	metricsSrv := &http.Server{Addr: ":" + cfg.MetricsPort, Handler: promhttp.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = metricsSrv.ListenAndServe() }()
	if len(cfg.EtcdEndpoints) > 0 {
		go registry.Register(ctx, cfg.EtcdEndpoints, cfg.Service, config.Get("HTTP_PORT", "8080"))
	}
	go func() {
		slog.Info("gateway listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("gateway failed", "err", err)
			stop()
		}
	}()
	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	_ = metricsSrv.Shutdown(sctx)
	for _, cc := range conns {
		_ = cc.Close()
	}
}
