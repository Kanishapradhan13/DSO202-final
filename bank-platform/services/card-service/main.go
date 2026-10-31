// card-service issues debit cards and authorises simulated card purchases
// against the linked account's available balance.
package main

import (
	"google.golang.org/grpc"

	cardv1 "github.com/dso202/bank-platform/gen/go/bank/card/v1"
	"github.com/dso202/bank-platform/pkg/clients"
	"github.com/dso202/bank-platform/pkg/config"
	"github.com/dso202/bank-platform/pkg/db"
	"github.com/dso202/bank-platform/pkg/events"
	"github.com/dso202/bank-platform/pkg/server"
)

func main() {
	cfg := server.Init(svcName)
	gdb := db.MustOpen(cfg, &Card{}, &Authorization{})
	if cfg.MigrateOnly {
		return
	}
	cl := clients.New(svcName)
	defer cl.Close()
	svc := &cardServer{db: gdb, cl: cl, pepper: config.Get("CARD_PAN_PEPPER", "dev-only-pepper-change-me")}
	server.Run(server.Service{
		Config:   cfg,
		Register: func(s *grpc.Server) { cardv1.RegisterCardServiceServer(s, svc) },
		Workers:  []server.Worker{events.Dispatcher(gdb, cl).Run},
	})
}
