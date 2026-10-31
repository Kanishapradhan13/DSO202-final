// auth-service owns credentials, sessions, MFA/OTP and roles, and issues RS256 JWTs.
package main

import (
	"context"
	"time"

	"google.golang.org/grpc"

	authv1 "github.com/dso202/bank-platform/gen/go/bank/auth/v1"
	"github.com/dso202/bank-platform/pkg/clients"
	"github.com/dso202/bank-platform/pkg/config"
	"github.com/dso202/bank-platform/pkg/db"
	"github.com/dso202/bank-platform/pkg/events"
	"github.com/dso202/bank-platform/pkg/server"
)

func main() {
	cfg := server.Init("auth-service")
	gdb := db.MustOpen(cfg, &User{}, &OTP{}, &RefreshToken{}, &SigningKey{})
	if cfg.MigrateOnly {
		return
	}
	keys, err := loadOrCreateKey(gdb, config.Get("JWT_PRIVATE_KEY_FILE", ""))
	if err != nil {
		panic(err)
	}
	svc := &authServer{
		db:         gdb,
		keys:       keys,
		accessTTL:  config.Duration("ACCESS_TOKEN_TTL", 15*time.Minute),
		refreshTTL: config.Duration("REFRESH_TOKEN_TTL", 7*24*time.Hour),
		exposeOTP:  config.Bool("DEV_EXPOSE_OTP", cfg.IsDev()),
	}
	if config.Bool("SEED_DEMO_USERS", true) {
		svc.seedStaff(context.Background(), config.Get("SEED_STAFF_PASSWORD", "Bank@12345"))
	}
	cl := clients.New(cfg.Service)
	defer cl.Close()
	server.Run(server.Service{
		Config:   cfg,
		Register: func(s *grpc.Server) { authv1.RegisterAuthServiceServer(s, svc) },
		Workers:  []server.Worker{events.Dispatcher(gdb, cl).Run, svc.cleanup},
	})
}
