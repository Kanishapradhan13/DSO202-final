// account-service owns account lifecycle, status, limits and fund holds.
package main

import (
	"time"

	"google.golang.org/grpc"

	accountv1 "github.com/dso202/bank-platform/gen/go/bank/account/v1"
	"github.com/dso202/bank-platform/pkg/clients"
	"github.com/dso202/bank-platform/pkg/config"
	"github.com/dso202/bank-platform/pkg/db"
	"github.com/dso202/bank-platform/pkg/events"
	"github.com/dso202/bank-platform/pkg/server"
)

func main() {
	cfg := server.Init(svcName)
	gdb := db.MustOpen(cfg, &Account{}, &Hold{})
	if cfg.MigrateOnly {
		return
	}
	cl := clients.New(svcName)
	defer cl.Close()
	svc := &accountServer{
		db:           gdb,
		cl:           cl,
		defaultLimit: config.Int("DEFAULT_DAILY_TRANSFER_LIMIT", 20_000_000), // Nu. 200,000
		holdTTL:      config.Duration("HOLD_TTL", 7*24*time.Hour),
	}
	server.Run(server.Service{
		Config:   cfg,
		Register: func(s *grpc.Server) { accountv1.RegisterAccountServiceServer(s, svc) },
		Workers:  []server.Worker{events.Dispatcher(gdb, cl).Run, svc.expireHolds},
	})
}
