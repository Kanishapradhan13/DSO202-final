// loan-service handles fixed-rate, fixed-term loans with equal monthly instalments.
package main

import (
	"log/slog"

	"google.golang.org/grpc"
	"gorm.io/gorm/clause"

	loanv1 "github.com/dso202/bank-platform/gen/go/bank/loan/v1"
	"github.com/dso202/bank-platform/pkg/clients"
	"github.com/dso202/bank-platform/pkg/db"
	"github.com/dso202/bank-platform/pkg/events"
	"github.com/dso202/bank-platform/pkg/server"
)

var seedProducts = []Product{
	{ID: "personal", Name: "Personal Loan", AnnualRateBps: 1300, MinAmount: 1_000_000, MaxAmount: 100_000_000, MinTerm: 6, MaxTerm: 60,
		Description: "Unsecured loan for personal needs, up to Nu. 1,000,000."},
	{ID: "education", Name: "Education Loan", AnnualRateBps: 900, MinAmount: 5_000_000, MaxAmount: 200_000_000, MinTerm: 12, MaxTerm: 84,
		Description: "Tuition and study costs at a concessional rate."},
	{ID: "vehicle", Name: "Vehicle Loan", AnnualRateBps: 1100, MinAmount: 10_000_000, MaxAmount: 300_000_000, MinTerm: 12, MaxTerm: 84,
		Description: "Finance a new or used vehicle."},
	{ID: "home", Name: "Home Loan", AnnualRateBps: 950, MinAmount: 50_000_000, MaxAmount: 1_000_000_000, MinTerm: 60, MaxTerm: 240,
		Description: "Build or buy a home, up to 20 years."},
}

func main() {
	cfg := server.Init(svcName)
	gdb := db.MustOpen(cfg, &Product{}, &Loan{}, &Instalment{})
	if cfg.MigrateOnly {
		return
	}
	if err := gdb.Clauses(clause.OnConflict{UpdateAll: true}).Create(&seedProducts).Error; err != nil {
		slog.Error("seed loan products", "err", err)
	}
	cl := clients.New(svcName)
	defer cl.Close()
	svc := &loanServer{db: gdb, cl: cl}
	server.Run(server.Service{
		Config:   cfg,
		Register: func(s *grpc.Server) { loanv1.RegisterLoanServiceServer(s, svc) },
		Workers:  []server.Worker{events.Dispatcher(gdb, cl).Run, svc.dueReminders},
	})
}
