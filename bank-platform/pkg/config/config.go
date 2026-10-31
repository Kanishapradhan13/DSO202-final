// Package config reads service configuration from environment variables.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the configuration common to every service.
type Config struct {
	Service     string
	GRPCPort    string
	MetricsPort string
	DatabaseURL string
	Env         string
	// EtcdEndpoints enables service registration and live config when set.
	EtcdEndpoints []string
	OTLPEndpoint  string
	MigrateOnly   bool
}

// Load reads the common configuration for the named service.
func Load(service string) Config {
	c := Config{
		Service:      service,
		GRPCPort:     Get("GRPC_PORT", "50051"),
		MetricsPort:  Get("METRICS_PORT", "9090"),
		DatabaseURL:  Get("DATABASE_URL", ""),
		Env:          Get("APP_ENV", "dev"),
		OTLPEndpoint: Get("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		MigrateOnly:  Bool("MIGRATE_ONLY", false),
	}
	if e := Get("ETCD_ENDPOINTS", ""); e != "" {
		c.EtcdEndpoints = strings.Split(e, ",")
	}
	return c
}

// IsDev reports whether the service runs in a development environment.
func (c Config) IsDev() bool { return c.Env == "dev" || c.Env == "local" }

// Get returns the environment variable key or def when unset.
func Get(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// Int returns the integer environment variable key or def.
func Int(key string, def int64) int64 {
	if v, err := strconv.ParseInt(os.Getenv(key), 10, 64); err == nil {
		return v
	}
	return def
}

// Bool returns the boolean environment variable key or def.
func Bool(key string, def bool) bool {
	if v, err := strconv.ParseBool(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

// Duration returns the duration environment variable key or def.
func Duration(key string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(key)); err == nil {
		return v
	}
	return def
}
