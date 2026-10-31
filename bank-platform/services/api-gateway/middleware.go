package main

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"golang.org/x/time/rate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	authv1 "github.com/dso202/bank-platform/gen/go/bank/auth/v1"
	"github.com/dso202/bank-platform/pkg/authz"
	"github.com/dso202/bank-platform/pkg/config"
	"github.com/dso202/bank-platform/pkg/registry"
)

type ctxKey int

const (
	principalKey ctxKey = iota
	requestIDKey
	clientIPKey
	userHolderKey
)

type principal struct {
	UserID, Email, Name string
	Roles               []string
}

func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": msg})
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				slog.Error("panic", "panic", v, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

var trustProxy = config.Bool("TRUST_PROXY", true)

func clientIP(r *http.Request) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// requestID attaches a correlation ID to every request and response.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if _, err := uuid.Parse(id); err != nil {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-Id", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		ctx = context.WithValue(ctx, clientIPKey, clientIP(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "bank_gateway_requests_total", Help: "Gateway requests by route, method and status.",
	}, []string{"route", "method", "code"})
	httpLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "bank_gateway_request_duration_seconds", Help: "Gateway request latency.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
	}, []string{"route", "method"})
	rateLimited = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bank_gateway_rate_limited_total", Help: "Requests rejected by the rate limiter.",
	})
)

// routeLabel collapses IDs so metric cardinality stays bounded.
func routeLabel(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, p := range parts {
		if _, err := uuid.Parse(p); err == nil {
			parts[i] = "{id}"
		} else if _, err := strconv.ParseInt(p, 10, 64); err == nil && len(p) > 4 {
			parts[i] = "{n}"
		}
	}
	if len(parts) > 5 {
		parts = parts[:5]
	}
	return "/" + strings.Join(parts, "/")
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(c int) {
	s.code = c
	s.ResponseWriter.WriteHeader(c)
}

func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: 200}
		// authenticate runs further down the chain; it records the user here for the access log.
		uid := new(string)
		next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), userHolderKey, uid)))
		route := routeLabel(r.URL.Path)
		httpRequests.WithLabelValues(route, r.Method, strconv.Itoa(rec.code)).Inc()
		httpLatency.WithLabelValues(route, r.Method).Observe(time.Since(start).Seconds())
		reqID, _ := r.Context().Value(requestIDKey).(string)
		slog.InfoContext(r.Context(), "http", "method", r.Method, "route", route, "status", rec.code,
			"duration_ms", time.Since(start).Milliseconds(), "request_id", reqID, "user_id", *uid)
	})
}

func cors(origins []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if o := r.Header.Get("Origin"); o != "" && slices.Contains(origins, o) {
				w.Header().Set("Access-Control-Allow-Origin", o)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, X-Request-Id")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Expose-Headers", "X-Request-Id")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func bodyLimit(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

// ---- Rate limiting --------------------------------------------------------

type limiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	rps      *registry.IntSetting
	authRate rate.Limit
}

type bucket struct {
	l    *rate.Limiter
	rps  int64
	seen time.Time
}

// newLimiter allows rps requests per second per caller (live-tunable at
// /bank/config/gateway/rate_limit_rps) and AUTH_RATE_PER_MIN per IP on sign-in routes.
func newLimiter(ctx context.Context, etcd []string) *limiter {
	l := &limiter{
		buckets:  map[string]*bucket{},
		rps:      registry.WatchInt(ctx, etcd, "/bank/config/gateway/rate_limit_rps", config.Int("RATE_LIMIT_RPS", 30)),
		authRate: rate.Every(time.Minute / time.Duration(max(1, config.Int("AUTH_RATE_PER_MIN", 10)))),
	}
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				l.mu.Lock()
				for k, b := range l.buckets {
					if time.Since(b.seen) > 10*time.Minute {
						delete(l.buckets, k)
					}
				}
				l.mu.Unlock()
			}
		}
	}()
	return l
}

var sensitiveAuthRoutes = []string{"/v1/auth/login", "/v1/auth/register", "/v1/auth/verify-email", "/v1/auth/mfa/verify", "/v1/auth/resend-code"}

func (l *limiter) allow(key string, sensitive bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	rps := l.rps.Get()
	b, ok := l.buckets[key]
	if !ok || (!sensitive && b.rps != rps) {
		if sensitive {
			b = &bucket{l: rate.NewLimiter(l.authRate, 10)}
		} else {
			b = &bucket{l: rate.NewLimiter(rate.Limit(rps), int(rps*2)), rps: rps}
		}
		l.buckets[key] = b
	}
	b.seen = time.Now()
	return b.l.Allow()
}

func (l *limiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _ := r.Context().Value(clientIPKey).(string)
		sensitive := r.Method == http.MethodPost && slices.Contains(sensitiveAuthRoutes, r.URL.Path)
		key := "ip:" + ip
		if sensitive {
			key = "auth:" + ip
		}
		if !l.allow(key, sensitive) {
			rateLimited.Inc()
			w.Header().Set("Retry-After", "5")
			writeError(w, http.StatusTooManyRequests, "too many requests, slow down")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- Authentication -------------------------------------------------------

type jwks struct {
	auth      authv1.AuthServiceClient
	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey
	lastFetch time.Time
}

func newJWKS(c authv1.AuthServiceClient) *jwks { return &jwks{auth: c, keys: map[string]*rsa.PublicKey{}} }

func (j *jwks) refresh(ctx context.Context) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if time.Since(j.lastFetch) < 10*time.Second && len(j.keys) > 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	resp, err := j.auth.GetJwks(ctx, &authv1.GetJwksRequest{})
	if err != nil {
		return err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range resp.Keys {
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	j.keys, j.lastFetch = keys, time.Now()
	return nil
}

func (j *jwks) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	j.mu.RLock()
	k, ok := j.keys[kid]
	j.mu.RUnlock()
	if ok {
		return k, nil
	}
	if err := j.refresh(ctx); err != nil {
		return nil, err
	}
	j.mu.RLock()
	defer j.mu.RUnlock()
	if k, ok := j.keys[kid]; ok {
		return k, nil
	}
	return nil, errors.New("unknown signing key")
}

type claims struct {
	Email string   `json:"email"`
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
	jwt.RegisteredClaims
}

var publicRoutes = map[string]bool{
	"POST /v1/auth/register":     true,
	"POST /v1/auth/verify-email": true,
	"POST /v1/auth/resend-code":  true,
	"POST /v1/auth/login":        true,
	"POST /v1/auth/mfa/verify":   true,
	"POST /v1/auth/refresh":      true,
	"POST /v1/auth/logout":       true,
	"GET /v1/auth/jwks":          true,
}

func authenticate(keys *jwks) func(http.Handler) http.Handler {
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer("bank-auth"),
		jwt.WithAudience("bank-api"), jwt.WithExpirationRequired(), jwt.WithLeeway(5*time.Second))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if publicRoutes[r.Method+" "+r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") {
				writeError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			var c claims
			_, err := parser.ParseWithClaims(strings.TrimPrefix(h, "Bearer "), &c, func(t *jwt.Token) (any, error) {
				kid, _ := t.Header["kid"].(string)
				return keys.key(r.Context(), kid)
			})
			if err != nil || c.Subject == "" {
				msg := "invalid token"
				if errors.Is(err, jwt.ErrTokenExpired) {
					msg = "token expired"
				}
				writeError(w, http.StatusUnauthorized, msg)
				return
			}
			p := &principal{UserID: c.Subject, Email: c.Email, Name: c.Name, Roles: c.Roles}
			if h, ok := r.Context().Value(userHolderKey).(*string); ok {
				*h = p.UserID
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, p)))
		})
	}
}

// ---- Authorization (coarse, by route prefix; services re-check) ----------

type rule struct {
	prefix  string
	methods []string // empty = any
	roles   []string
}

var rbac = []rule{
	{"/v1/admin/", nil, []string{authz.RoleAdmin}},
	{"/v1/staff/transactions/", []string{"POST"}, []string{authz.RoleOpsOfficer}},
	{"/v1/staff/transactions", nil, []string{authz.RoleOpsOfficer, authz.RoleFraudAnalyst, authz.RoleAuditor}},
	{"/v1/staff/kyc", nil, []string{authz.RoleOpsOfficer, authz.RoleLoanOfficer, authz.RoleAuditor}},
	{"/v1/staff/loans", nil, []string{authz.RoleLoanOfficer, authz.RoleAuditor}},
	{"/v1/staff/fraud", nil, []string{authz.RoleFraudAnalyst, authz.RoleAuditor}},
	{"/v1/staff/audit", nil, []string{authz.RoleAuditor, authz.RoleFraudAnalyst}},
	{"/v1/staff/ledger", nil, []string{authz.RoleAuditor}},
	{"/v1/staff/statements", nil, []string{authz.RoleAdmin}},
	{"/v1/staff/", nil, authz.StaffRoles},
}

func authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := r.Context().Value(principalKey).(*principal)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		for _, ru := range rbac {
			if !strings.HasPrefix(r.URL.Path, ru.prefix) || (len(ru.methods) > 0 && !slices.Contains(ru.methods, r.Method)) {
				continue
			}
			if !(authz.Principal{Roles: p.Roles}).HasRole(ru.roles...) {
				writeError(w, http.StatusForbidden, "you do not have access to this resource")
				return
			}
			break
		}
		next.ServeHTTP(w, r)
	})
}

// ---- grpc-gateway hooks ---------------------------------------------------

// headerMatcher forwards only the headers services need. Identity headers are
// never taken from the client; they come from the verified token.
func headerMatcher(key string) (string, bool) {
	switch strings.ToLower(key) {
	case "idempotency-key":
		return authz.MDIdempotencyKey, true
	case "traceparent", "tracestate":
		return strings.ToLower(key), true
	}
	return "", false
}

func principalMetadata(ctx context.Context, r *http.Request) metadata.MD {
	md := metadata.MD{}
	if id, ok := r.Context().Value(requestIDKey).(string); ok {
		md.Set(authz.MDRequestID, id)
	}
	if ip, ok := r.Context().Value(clientIPKey).(string); ok {
		md.Set(authz.MDClientIP, ip)
	}
	if p, ok := r.Context().Value(principalKey).(*principal); ok {
		md.Set(authz.MDUserID, p.UserID)
		md.Set(authz.MDUserRoles, strings.Join(p.Roles, ","))
		md.Set(authz.MDUserEmail, p.Email)
		md.Set(authz.MDUserName, p.Name)
	}
	return md
}

func errorHandler(ctx context.Context, mux *runtime.ServeMux, m runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
	st := status.Convert(err)
	code := runtime.HTTPStatusFromCode(st.Code())
	msg := st.Message()
	switch st.Code() {
	case codes.Internal, codes.Unknown, codes.DataLoss:
		// Never leak internal error text to clients; log it against the request ID.
		reqID, _ := r.Context().Value(requestIDKey).(string)
		slog.ErrorContext(ctx, "upstream error", "path", r.URL.Path, "code", st.Code().String(), "err", msg, "request_id", reqID)
		msg = fmt.Sprintf("something went wrong (reference %s)", reqID)
	case codes.Unavailable:
		if strings.Contains(msg, "connection") || strings.Contains(msg, "dial") || msg == "" {
			msg = "service temporarily unavailable, please retry"
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "status": st.Code().String(), "message": msg})
}

func routingErrorHandler(ctx context.Context, mux *runtime.ServeMux, m runtime.Marshaler, w http.ResponseWriter, r *http.Request, code int) {
	writeError(w, code, http.StatusText(code))
}
