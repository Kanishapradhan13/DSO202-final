// transfer-service orchestrates internal transfers as a saga and manages payees.
package main

import (
	"context"

	"google.golang.org/grpc"

	transferv1 "github.com/dso202/bank-platform/gen/go/bank/transfer/v1"
	"github.com/dso202/bank-platform/pkg/clients"
	"github.com/dso202/bank-platform/pkg/db"
	"github.com/dso202/bank-platform/pkg/events"
	"github.com/dso202/bank-platform/pkg/registry"
	"github.com/dso202/bank-platform/pkg/server"
)

func main() {
	cfg := server.Init(svcName)
	gdb := db.MustOpen(cfg, &Transfer{}, &Payee{})
	if cfg.MigrateOnly {
		return
	}
	cl := clients.New(svcName)
	defer cl.Close()
	ctx := context.Background()
	svc := &transferServer{
		db: gdb,
		cl: cl,
		// Live-tunable from etcd: /bank/config/transfer/daily_limit (chhertrum).
		dailyCap: registry.WatchInt(ctx, cfg.EtcdEndpoints, "/bank/config/transfer/daily_limit", 50_000_000),
	}
	server.Run(server.Service{
		Config:   cfg,
		Register: func(s *grpc.Server) { transferv1.RegisterTransferServiceServer(s, svc) },
		Workers:  []server.Worker{events.Dispatcher(gdb, cl).Run, svc.recover},
	})
}
