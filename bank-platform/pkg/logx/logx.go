// Package logx configures JSON structured logging with trace correlation, so
// Grafana can jump from a Loki log line to its Jaeger trace.
package logx

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(a []slog.Attr) slog.Handler { return traceHandler{h.Handler.WithAttrs(a)} }
func (h traceHandler) WithGroup(n string) slog.Handler      { return traceHandler{h.Handler.WithGroup(n)} }

// Setup installs the default logger for the named service.
func Setup(service string) {
	level := slog.LevelInfo
	if strings.EqualFold(os.Getenv("LOG_LEVEL"), "debug") {
		level = slog.LevelDebug
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(traceHandler{h}).With("service", service))
}

// MaskEmail hides most of an email address for logs.
func MaskEmail(e string) string {
	at := strings.IndexByte(e, '@')
	if at <= 1 {
		return "***"
	}
	return e[:1] + "***" + e[at:]
}
