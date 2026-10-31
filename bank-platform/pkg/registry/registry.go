// Package registry registers service instances in etcd under a lease and
// exposes watched configuration keys:
//
//	/bank/services/<service>/<host:port>   instance registration (10 s lease)
//	/bank/config/<service>/<key>           live configuration
//	/bank/flags/<flag>                     feature flags
package registry

import (
	"context"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

const leaseTTL = 10

func hostIP() string {
	if ip := os.Getenv("POD_IP"); ip != "" {
		return ip
	}
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return "unknown"
}

func newClient(endpoints []string) (*clientv3.Client, error) {
	return clientv3.New(clientv3.Config{Endpoints: endpoints, DialTimeout: 5 * time.Second})
}

// Register keeps this instance registered until ctx is cancelled, re-creating the
// lease if etcd restarts.
func Register(ctx context.Context, endpoints []string, service, port string) {
	key := "/bank/services/" + service + "/" + net.JoinHostPort(hostIP(), port)
	for ctx.Err() == nil {
		if err := registerOnce(ctx, endpoints, key); err != nil {
			slog.Warn("etcd registration lost, retrying", "key", key, "err", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
}

func registerOnce(ctx context.Context, endpoints []string, key string) error {
	cli, err := newClient(endpoints)
	if err != nil {
		return err
	}
	defer cli.Close()
	lease, err := cli.Grant(ctx, leaseTTL)
	if err != nil {
		return err
	}
	if _, err := cli.Put(ctx, key, time.Now().UTC().Format(time.RFC3339), clientv3.WithLease(lease.ID)); err != nil {
		return err
	}
	ch, err := cli.KeepAlive(ctx, lease.ID)
	if err != nil {
		return err
	}
	slog.Info("registered in etcd", "key", key)
	for range ch {
	}
	// Revoke on clean shutdown so the instance disappears immediately.
	rctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = cli.Revoke(rctx, lease.ID)
	return ctx.Err()
}

// Instances lists registered instances of a service.
func Instances(ctx context.Context, endpoints []string, service string) ([]string, error) {
	cli, err := newClient(endpoints)
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	resp, err := cli.Get(ctx, "/bank/services/"+service+"/", clientv3.WithPrefix(), clientv3.WithKeysOnly())
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		out = append(out, string(kv.Key))
	}
	return out, nil
}

// IntSetting is an integer config value kept in sync with an etcd key.
// Without etcd it simply holds its default.
type IntSetting struct {
	mu  sync.RWMutex
	val int64
}

// Get returns the current value.
func (s *IntSetting) Get() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.val
}

func (s *IntSetting) set(v int64) {
	s.mu.Lock()
	s.val = v
	s.mu.Unlock()
}

// WatchInt returns a setting initialised to def and, when etcd is configured,
// updated whenever key changes. No restart is needed for config changes.
func WatchInt(ctx context.Context, endpoints []string, key string, def int64) *IntSetting {
	s := &IntSetting{val: def}
	if len(endpoints) == 0 {
		return s
	}
	go func() {
		for ctx.Err() == nil {
			watchOnce(ctx, endpoints, key, def, s)
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
			}
		}
	}()
	return s
}

func watchOnce(ctx context.Context, endpoints []string, key string, def int64, s *IntSetting) {
	cli, err := newClient(endpoints)
	if err != nil {
		return
	}
	defer cli.Close()
	parse := func(b []byte) {
		if v, err := strconv.ParseInt(string(b), 10, 64); err == nil {
			s.set(v)
			slog.Info("config updated from etcd", "key", key, "value", v)
		}
	}
	resp, err := cli.Get(ctx, key)
	if err != nil {
		return
	}
	if len(resp.Kvs) == 0 {
		// Seed the key so operators can see and change it.
		_, _ = cli.Put(ctx, key, strconv.FormatInt(def, 10))
	} else {
		parse(resp.Kvs[0].Value)
	}
	for wr := range cli.Watch(ctx, key) {
		for _, ev := range wr.Events {
			if ev.Type == clientv3.EventTypeDelete {
				s.set(def)
				continue
			}
			parse(ev.Kv.Value)
		}
	}
}
