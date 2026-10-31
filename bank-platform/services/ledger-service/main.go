// ledger-service is the double-entry journal and the single source of truth for money.
package main

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"

	ledgerv1 "github.com/dso202/bank-platform/gen/go/bank/ledger/v1"
	"github.com/dso202/bank-platform/pkg/db"
	"github.com/dso202/bank-platform/pkg/ledgerx"
	"github.com/dso202/bank-platform/pkg/server"
)

func main() {
	cfg := server.Init("ledger-service")
	gdb := db.MustOpen(cfg, &LedgerAccount{}, &JournalEntry{}, &Posting{})
	if cfg.MigrateOnly {
		return
	}
	svc := &ledgerServer{db: gdb}
	for id, name := range ledgerx.SystemAccounts {
		if _, err := svc.ensureAccount(context.Background(), id, KindSystem, name); err != nil {
			slog.Error("seed system account", "id", id, "err", err)
		}
	}
	server.Run(server.Service{
		Config:   cfg,
		Register: func(s *grpc.Server) { ledgerv1.RegisterLedgerServiceServer(s, svc) },
	})
}
