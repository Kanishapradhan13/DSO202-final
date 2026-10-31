// statement-service builds monthly and on-demand account statements (PDF/CSV)
// from ledger postings, and proves each one reconciles to the ledger.
//
// Run "statement-service run-monthly" to generate last month's statements for
// every account and exit (used by the Kubernetes CronJob).
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"google.golang.org/grpc"

	statementv1 "github.com/dso202/bank-platform/gen/go/bank/statement/v1"
	"github.com/dso202/bank-platform/pkg/authz"
	"github.com/dso202/bank-platform/pkg/clients"
	"github.com/dso202/bank-platform/pkg/db"
	"github.com/dso202/bank-platform/pkg/events"
	"github.com/dso202/bank-platform/pkg/server"
)

func main() {
	cfg := server.Init(svcName)
	gdb := db.MustOpen(cfg, &Statement{})
	if cfg.MigrateOnly {
		return
	}
	cl := clients.New(svcName)
	defer cl.Close()
	svc := &statementServer{db: gdb, cl: cl}

	if len(os.Args) > 1 && os.Args[1] == "run-monthly" {
		prev := time.Now().UTC().AddDate(0, -1, 0)
		ctx, cancel := context.WithTimeout(authz.SystemContext(context.Background()), 30*time.Minute)
		defer cancel()
		res, err := svc.runMonthly(ctx, int32(prev.Year()), int32(prev.Month()))
		if err != nil {
			slog.Error("monthly run failed", "err", err)
			os.Exit(1)
		}
		// Deliver queued notifications before exiting.
		d := events.Dispatcher(gdb, cl)
		dctx, dcancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer dcancel()
		go d.Run(dctx)
		<-dctx.Done()
		slog.Info("monthly run complete", "generated", res.Generated, "skipped", res.Skipped, "failed", res.Failed)
		return
	}

	server.Run(server.Service{
		Config:   cfg,
		Register: func(s *grpc.Server) { statementv1.RegisterStatementServiceServer(s, svc) },
		Workers:  []server.Worker{events.Dispatcher(gdb, cl).Run},
	})
}
