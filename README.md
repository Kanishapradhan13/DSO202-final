# CBMP Bank: Core Banking Microservices Platform

DSO202 final project. A working retail bank built as **16 services**: a Next.js portal, a Go API gateway and
**14 Go gRPC domain services**. Each domain service owns its own PostgreSQL database through GORM.

```
Browser ──HTTPS──▶ web-portal (Next.js BFF, httpOnly cookies)
                       │ REST + JWT
                       ▼
                  api-gateway (Go · grpc-gateway · JWT/RBAC · rate limit · request IDs)
                       │ gRPC
   ┌──────────┬────────┼─────────┬──────────┬──────────┬────────────┐
 auth   customer  kyc  account  ledger  transaction  transfer  payment
 card   loan   fraud   audit   notification   statement
   │ each: own PostgreSQL DB · outbox · Prometheus /metrics · OTel traces · etcd registration
```

| # | Service | Responsibility | DB |
|---|---------|----------------|----|
| 1 | web-portal | Customer and back-office UI; backend-for-frontend that keeps tokens out of the browser | — |
| 2 | api-gateway | REST→gRPC, RS256 JWT validation (JWKS from auth), route RBAC, rate limits, correlation IDs | — |
| 3 | auth-service | Register, email OTP, login + MFA, rotating refresh tokens, lockout, roles, staff admin | auth_db |
| 4 | customer-service | Profile and CIF number | customer_db |
| 5 | kyc-service | Document capture, automated checks (mock provider), officer review | kyc_db |
| 6 | account-service | Open/freeze/close accounts, KYC gate, **fund holds** under row lock | account_db |
| 7 | ledger-service | **Double-entry journal**, the single source of truth for balances, reversals, trial balance | ledger_db |
| 8 | transaction-service | Teller deposits/withdrawals; transaction-history read model; low-balance alerts | transaction_db |
| 9 | transfer-service | Transfer **saga** (limit → fraud → hold → post → release), payees, crash recovery | transfer_db |
| 10 | payment-service | Bill payments via a mocked external gateway (saga) | payment_db |
| 11 | card-service | Debit cards (tokenised PAN, bcrypt PIN), limits, block, simulated authorisations | card_db |
| 12 | loan-service | Products, EMI quotes and schedules, officer approval + disbursement, repayments | loan_db |
| 13 | fraud-service | Configurable rules (amount, velocity, new payee, daily volume, night); review queue | fraud_db |
| 14 | audit-service | Append-only, **SHA-256 hash-chained** audit log (DB trigger blocks UPDATE/DELETE) | audit_db |
| 15 | notification-service | Email (SMTP → Mailpit), simulated SMS, in-app inbox; idempotent per event | notification_db |
| 16 | statement-service | PDF/CSV statements reconciled against the ledger; `run-monthly` batch for a CronJob | statement_db |

## Quick start

Requires Docker (about 3 GB free RAM). From this folder:

```bash
docker compose up -d --build        # first build takes a few minutes
./scripts/smoke-test.sh             # 30 end-to-end checks through the gateway
```

| URL | What |
|-----|------|
| http://localhost:3000 | Web portal |
| http://localhost:8080 | API gateway (REST) |
| http://localhost:8025 | Mailpit: OTP codes and notification emails |
| localhost:5433 | PostgreSQL (`postgres` / `postgres`) |

Faster rebuilds with Go installed on the host: `make up-fast` compiles the binaries locally and copies them into
distroless images.

### Demo accounts

Staff accounts are seeded with the password `Bank@12345`:

| Email | Role | Can |
|-------|------|-----|
| admin@bank.local | admin | everything, create staff, run monthly statements |
| ops@bank.local | ops_officer | approve KYC, teller deposit/withdraw, freeze accounts |
| loans@bank.local | loan_officer | approve/reject and disburse loans |
| fraud@bank.local | fraud_analyst | release/reject held transactions, tune rules |
| auditor@bank.local | auditor | audit trail, hash-chain verification, trial balance |

Customers self-register in the portal. In dev mode the email code is shown on screen and is also in Mailpit.

### Demo script (about 5 minutes)

1. **Register** a customer → verify the code → fill in the profile → submit KYC (CID = 11 digits).
2. Sign in as **ops@** → *KYC reviews* → approve. Back as the customer → open a savings account.
3. As **ops@** → *Customers & teller* → find the customer → **Deposit** Nu. 300,000.
4. Customer → *Transfer* Nu. 2,500 to a second customer. The result screen shows each **saga step**.
5. Transfer **Nu. 120,000**: fraud rule `AMOUNT_REVIEW` holds it (`PENDING_REVIEW`, funds on hold).
   Sign in as **fraud@** → *Fraud & risk* → Release. The transfer completes and the hold is released.
6. *Pay bills*: a reference containing `FAIL` shows a clean failure with no money lost.
7. *Cards*: issue → activate with PIN → simulate a purchase (try a wrong PIN three times).
8. *Loans*: apply → **loans@** approves (disbursed via ledger) → customer pays an EMI.
9. Account → *Statements* → generate → download the PDF (marked *Reconciled*).
10. **auditor@** → *Audit & ledger*: trial balance **balanced**, **Verify hash chain**: intact.

## How correctness is guaranteed

- **Ledger as the only balance owner.** Money is int64 chhertrum (Nu. 1 = 100 Ch). Every movement is a balanced
  journal entry (Σdebits = Σcredits), written in one DB transaction. Accounts are locked in sorted order, so concurrent
  entries cannot deadlock, and customer accounts can never go below zero. Corrections are reversing entries.
- **Idempotency everywhere.** Clients send `Idempotency-Key`. Transfers, payments, deposits and card authorisations
  are unique per (user, key), ledger entries are unique per key, and loan EMIs use a per-instalment key.
  Replaying a request returns the original result and posts nothing (`smoke-test.sh` checks this).
- **Holds prevent overspending.** `PlaceHold` locks the account row and checks *ledger balance − active holds*.
  `scripts/race-test.sh` fires 10 concurrent Nu. 300 transfers at a Nu. 1,000 account: exactly 3 succeed.
- **Sagas with compensation, no 2PC.** On failure, earlier steps are undone (the hold is released). A recovery
  worker resumes sagas stuck by a crash; every step is idempotent, so re-running is safe.
- **Transactional outbox.** Audit records, notifications and history lines are written in the same DB
  transaction as the business change, then delivered with retries (`FOR UPDATE SKIP LOCKED`, safe with replicas).
  A notification or audit outage never blocks or loses a payment.
- **Fraud check fails closed.** If fraud-service is unavailable, the transfer fails; money does not move unscored.

## Security

- RS256 JWTs (15 min) plus rotating refresh tokens. Reusing a rotated token revokes all of that user's sessions.
  Argon2id passwords, lockout after 5 failures, optional email MFA, OTPs stored as hashes.
- Browser JavaScript never sees tokens. The portal keeps them in httpOnly SameSite cookies, rejects cross-origin
  writes and refreshes transparently.
- The gateway takes identity only from the verified token. It never forwards client `x-user-*`, `Grpc-Metadata-*`
  or `x-internal-caller` headers.
- **Internal-only RPCs** (any method without a `google.api.http` route, such as `LedgerService/PostEntries`) are
  rejected unless called by another service. This is enforced by a shared interceptor that reads the proto descriptors.
- Defence in depth: services re-check roles and ownership. For example, customer B gets 404 for A's account.
- Cards: PAN never stored (token + peppered hash + last 4), PIN bcrypt, auto-block after 3 wrong PINs.
- PII masked in logs (emails, phones, document numbers in lists).

## Observability and platform hooks

| Concern | Where |
|---------|-------|
| Metrics | every service `:9090/metrics`: `grpc_server_handled_total`, `grpc_server_handling_seconds`, plus business metrics: `bank_transfers_total{status}`, `bank_transfer_saga_seconds`, `bank_ledger_value_posted_chhertrum_total`, `bank_fraud_decisions_total`, `bank_card_authorizations_total`, `bank_gateway_requests_total`, `bank_gateway_rate_limited_total` |
| Traces | OpenTelemetry (W3C traceparent) through gateway → gRPC → SQL. `OTEL_ENDPOINT=jaeger:4317 docker compose --profile observability up -d` → http://localhost:16686 |
| Logs | JSON on stdout with `trace_id`, `request_id`, `user_id` (ready for Loki) |
| Health | gRPC health service on every service (`grpc_health_probe`/K8s `grpc:` probes); gateway `/healthz`, `/readyz` |
| etcd | registration `/bank/services/<svc>/<ip:port>` (10 s lease). Live config with no restart: `/bank/config/transfer/daily_limit`, `/bank/config/gateway/rate_limit_rps` |
| Migrations | `MIGRATE_ONLY=true` runs GORM migrations and exits (Argo CD PreSync Job) |
| CronJob | `statement-service run-monthly` generates last month's statements and exits |

Try live config: `docker compose exec etcd etcdctl put /bank/config/transfer/daily_limit 100000` caps every
account at Nu. 1,000/day immediately.

## Configuration

| Variable | Default | Used by |
|----------|---------|---------|
| `DATABASE_URL` | — | every domain service |
| `GRPC_PORT` / `METRICS_PORT` | 50051 / 9090 | every service |
| `<NAME>_ADDR` | `<name>-service:50051` | callers, e.g. `LEDGER_ADDR` |
| `ETCD_ENDPOINTS` | unset (disabled) | all |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | unset (disabled) | all |
| `APP_ENV` | dev | `dev` exposes OTP codes in API messages; set `prod` to disable |
| `JWT_PRIVATE_KEY_FILE` | unset → key generated and kept in auth_db | auth |
| `SEED_DEMO_USERS` / `SEED_STAFF_PASSWORD` | true / Bank@12345 | auth |
| `SMTP_ADDR` | unset (logs only) | notification |
| `CARD_PAN_PEPPER` | dev value | card (set a secret!) |
| `RATE_LIMIT_RPS` / `AUTH_RATE_PER_MIN` | 30 / 10 | gateway |
| `GATEWAY_URL`, `COOKIE_SECURE` | localhost:8080, false | web-portal |

## Repository layout

```
proto/bank/<svc>/v1/*.proto   gRPC contracts with REST annotations (source of truth)
gen/go/                       generated code (make proto)
pkg/                          shared service template: server, authz, clients, db, outbox, events, saga, registry, telemetry
services/<svc>/               one folder per service (main.go + domain logic + tests)
web/                          Next.js 15 portal (App Router, Tailwind 4)
deploy/docker/                Dockerfiles and Postgres init (14 DBs, 14 roles)
scripts/                      gen-proto, smoke-test, race-test
```

## Development

```bash
make proto      # regenerate after editing .proto (needs protoc + protoc-gen-go/-go-grpc/-grpc-gateway)
make test       # unit tests (ledger balancing, EMI amortisation, Luhn, audit hash chain, money formatting)
make smoke      # end-to-end against a running stack
cd web && npm run dev   # portal on :3000 against GATEWAY_URL
```

## Build report: what was done

This section records how the project was built, what was verified, and what is still open.

### 1. Requirements taken from the project Drive folder

The design follows the group's own documents in `DSO202-FINAL` on Google Drive:

- **Documentation/App**: the list of 15 services (gateway plus 14 microservices).
- **Proposal/PM-Document/Planning-Analysis.docx**: scope, functional needs HF-01 to HF-14, non-functional needs
  HN-01 to HN-14, and the core journey.
- **Proposal/slide.pptx**: the technology choices (Next.js, Go, gRPC, GORM, PostgreSQL per service, etcd, saga
  flow, int64 chhertrum).
- **diagrams/**: the high-level architecture (client → gateway → gRPC services → GORM).

### 2. What was built

| Area | Delivered |
|------|-----------|
| API contracts | 14 `.proto` files (`proto/bank/*/v1`) with REST annotations; generated Go, gRPC and grpc-gateway code (42 files) |
| Shared service template (`pkg/`) | gRPC server bootstrap (health, reflection, Prometheus, OTel, JSON logs, panic recovery, graceful shutdown); internal-method guard; identity propagation (`authz`); typed clients with retries and deadlines; GORM DB with retry and migrations; transactional outbox and dispatcher; audit/notify/history events; saga step log; etcd registration and live config; money and paging helpers |
| 14 domain services | auth, customer, kyc, account, ledger, transaction, transfer, payment, card, loan, fraud, audit, notification, statement (see the table at the top) |
| API gateway | REST→gRPC for all 14 services, RS256 JWT validation through JWKS, role-based route access, per-IP and sign-in rate limits, request IDs, metrics, a safe error model, and blocking of spoofed identity headers |
| Web portal (Next.js 15) | 23 routes. **Customer:** landing, register, email verify, login with MFA, onboarding (profile → KYC → account), dashboard, accounts and history with filters, statements (PDF/CSV), transfers with saga timeline and payees, bill pay, cards (issue, activate, limits, block, simulated purchase), loans (EMI calculator, apply, schedule, repay), notifications, settings. **Staff:** back-office home, KYC review, customers and teller, loan desk, fraud alerts and rule tuning, audit trail and trial balance, user admin. Tokens are kept in httpOnly cookies behind a backend-for-frontend proxy, with CSRF origin checks and silent token refresh |
| Containers | One multi-stage Dockerfile for every Go service, a faster prebuilt variant, the portal Dockerfile, `docker-compose.yml` (Postgres with 14 databases and 14 roles, etcd, Mailpit, optional Jaeger, 16 app containers) and a Makefile |
| Tests | Unit tests (ledger balancing, EMI amortisation, Luhn check digits, PAN hashing, audit hash chain, money and paging), `scripts/smoke-test.sh` (30 end-to-end checks) and `scripts/race-test.sh` (concurrency) |

### 3. Verification results (run on 30 Oct 2026)

| Check | Result |
|-------|--------|
| `go build ./...` and `go vet ./...` | clean |
| `go test ./...` | all pass |
| `npx tsc --noEmit` and `npm run build` (portal) | clean; 23 routes built |
| `docker compose up` (backend, 18 containers) | all running; every service registered in etcd |
| `scripts/smoke-test.sh` | **30 / 30 passed**: onboarding and KYC gate, teller deposit, transfer plus idempotent replay (money moved exactly once), insufficient funds, access isolation between customers, fraud review hold and analyst release, bill payment plus clean failure, card purchase plus wrong-PIN decline, loan disbursement plus EMI repayment, statement reconciled to the ledger with PDF download, ledger balanced, audit chain intact, internal RPCs not exposed |
| `scripts/race-test.sh` | 10 concurrent Nu. 300 transfers from a Nu. 1,000 account: **3 completed, 7 rejected, final balance exactly Nu. 100** (no overspend) |
| Security probes | spoofed `X-User-Id`, `Grpc-Metadata-*` role injection, a tampered JWT and a fake `x-internal-caller` header were all rejected (401/403) |
| Portal session flow | httpOnly cookies set, proxy works, cross-origin POST rejected, signed-out users redirected to login, logout revokes the session, an expired access token is refreshed silently |

### 4. Issues found and fixed during the build

- A failed OTP attempt was counted inside a transaction that then rolled back, so the 5-attempt limit never
  triggered. The attempt is now counted outside the transaction.
- The gateway's access log always showed an empty `user_id`. The authenticated user is now recorded for the log.
- Staff could simulate purchases on a customer's card. Only the cardholder can now authorise purchases.
- GORM's coloured slow-query output broke the JSON log format. It now logs plain text and only above 1 s.
- The sign-in rate limit is now configurable (`AUTH_RATE_PER_MIN`; 60 in docker-compose for demos).
- The brand was changed from a name close to a real Bhutanese bank to **CBMP Bank** (the project's own name).
- The `# syntax=` line was removed from the Dockerfiles because BuildKit could not fetch that frontend image
  on this network.
- The portal port is configurable (`WEB_PORT`, default 3000), because port 3000 was already in use on the
  development machine.

### 5. Still open

- **Portal container**: the portal builds and runs locally (tested with `next start`). Its Docker image was still
  building over a slow network when this report was written. Check with `docker compose ps web-portal`.
- **Multi-stage Go Dockerfile**: the first test failed only on the BuildKit syntax image, since removed. The
  verified path is `make up-fast` (`Dockerfile.prebuilt`). Re-test `docker compose up --build` once
  `golang:1.27-alpine` has been pulled.
- **Kubernetes layer from the proposal** is not built yet: Helm chart, Kustomize overlays, Argo CD apps, Istio
  mTLS and AuthorizationPolicies, NetworkPolicies, CloudNativePG, HPA on custom metrics, Grafana dashboards. The
  services already provide what that layer needs: gRPC health probes, `/metrics`, OTel, `MIGRATE_ONLY` for a
  PreSync Job, `run-monthly` for a CronJob, `POD_IP` and etcd registration.
- Not in scope, per the planning document: real payment, card or SMS integrations (all mocked), multi-currency,
  ML fraud models, and production use.

---

*Demonstration system for coursework. Synthetic data only; no real money moves.*

make up-fast && ./scripts/smoke-test.sh

cd ~/DSO202-Final/bank-platform
docker compose up -d $(docker compose config --services | grep -v -e web-portal -e jaeger)   # backend
cd web && GATEWAY_URL=http://localhost:8080 npx next start -p 3100                           # portal
