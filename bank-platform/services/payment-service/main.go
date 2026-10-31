// payment-service pays billers through a mocked external payment gateway.
package main

import (
	"log/slog"

	"google.golang.org/grpc"
	"gorm.io/gorm/clause"

	paymentv1 "github.com/dso202/bank-platform/gen/go/bank/payment/v1"
	"github.com/dso202/bank-platform/pkg/clients"
	"github.com/dso202/bank-platform/pkg/db"
	"github.com/dso202/bank-platform/pkg/events"
	"github.com/dso202/bank-platform/pkg/server"
)

var seedBillers = []Biller{
	{ID: "electricity", Name: "National Electricity Board", Category: "Utilities", ReferenceLabel: "Consumer number", ReferenceHint: "e.g. 100234567"},
	{ID: "water", Name: "City Water & Sewerage", Category: "Utilities", ReferenceLabel: "Connection number", ReferenceHint: "e.g. WS-55821"},
	{ID: "mobile", Name: "Mobile Prepaid Top-up", Category: "Telecom", ReferenceLabel: "Mobile number", ReferenceHint: "8-digit number"},
	{ID: "broadband", Name: "Fibre Broadband", Category: "Telecom", ReferenceLabel: "Customer ID", ReferenceHint: "on your monthly bill"},
	{ID: "property-tax", Name: "Municipal Property Tax", Category: "Government", ReferenceLabel: "Thram number", ReferenceHint: "land record number"},
	{ID: "tuition", Name: "University Tuition Fees", Category: "Education", ReferenceLabel: "Student ID", ReferenceHint: "e.g. 02230279"},
	{ID: "insurance", Name: "Life Insurance Premium", Category: "Insurance", ReferenceLabel: "Policy number", ReferenceHint: "e.g. LI-778812"},
}

func main() {
	cfg := server.Init(svcName)
	gdb := db.MustOpen(cfg, &Biller{}, &Payment{})
	if cfg.MigrateOnly {
		return
	}
	if err := gdb.Clauses(clause.OnConflict{UpdateAll: true}).Create(&seedBillers).Error; err != nil {
		slog.Error("seed billers", "err", err)
	}
	cl := clients.New(svcName)
	defer cl.Close()
	svc := &paymentServer{db: gdb, cl: cl, gateway: mockGateway{}}
	server.Run(server.Service{
		Config:   cfg,
		Register: func(s *grpc.Server) { paymentv1.RegisterPaymentServiceServer(s, svc) },
		Workers:  []server.Worker{events.Dispatcher(gdb, cl).Run, svc.recover},
	})
}
