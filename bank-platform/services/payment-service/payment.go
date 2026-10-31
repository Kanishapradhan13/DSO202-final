package main

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	accountv1 "github.com/dso202/bank-platform/gen/go/bank/account/v1"
	fraudv1 "github.com/dso202/bank-platform/gen/go/bank/fraud/v1"
	ledgerv1 "github.com/dso202/bank-platform/gen/go/bank/ledger/v1"
	paymentv1 "github.com/dso202/bank-platform/gen/go/bank/payment/v1"
	transactionv1 "github.com/dso202/bank-platform/gen/go/bank/transaction/v1"
	"github.com/dso202/bank-platform/pkg/authz"
	"github.com/dso202/bank-platform/pkg/clients"
	"github.com/dso202/bank-platform/pkg/events"
	"github.com/dso202/bank-platform/pkg/ledgerx"
	"github.com/dso202/bank-platform/pkg/saga"
	"github.com/dso202/bank-platform/pkg/util"
)

const svcName = "payment-service"

// Payment statuses.
const (
	Pending       = "PENDING"
	PendingReview = "PENDING_REVIEW"
	Success       = "SUCCESS"
	Failed        = "FAILED"
	Rejected      = "REJECTED"
)

var paymentsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "bank_payments_total", Help: "Bill payments by final status.",
}, []string{"status", "biller"})

// Biller is a payee organisation in the catalogue.
type Biller struct {
	ID             string `gorm:"primaryKey"`
	Name           string `gorm:"not null"`
	Category       string
	ReferenceLabel string
	ReferenceHint  string
}

// Payment is one bill payment order and its saga state.
type Payment struct {
	ID                string `gorm:"primaryKey;type:uuid"`
	UserID            string `gorm:"type:uuid;not null;uniqueIndex:idx_payment_idem;index"`
	IdempotencyKey    string `gorm:"not null;uniqueIndex:idx_payment_idem"`
	FromAccountID     string `gorm:"type:uuid;not null"`
	FromAccountNumber string
	BillerID          string `gorm:"not null"`
	BillerName        string
	CustomerReference string
	Amount            int64  `gorm:"not null"`
	Status            string `gorm:"index;not null"`
	FailureReason     string
	GatewayReference  string
	FraudDecision     string
	LedgerEntryID     string
	Steps             saga.Steps
	CreatedAt         time.Time `gorm:"index"`
	UpdatedAt         time.Time
}

// gateway is the external payment network.
type gateway interface {
	Pay(ctx context.Context, billerID, reference string, amount int64, idemKey string) (string, error)
}

// mockGateway simulates an external payment switch. References containing
// FAIL are declined, which lets the failure path be demonstrated.
type mockGateway struct{}

func (mockGateway) Pay(ctx context.Context, billerID, reference string, amount int64, idemKey string) (string, error) {
	select {
	case <-time.After(time.Duration(40+rand.IntN(120)) * time.Millisecond):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if strings.Contains(strings.ToUpper(reference), "FAIL") {
		return "", errors.New("biller declined: invalid or inactive reference")
	}
	return "PGW" + strings.ToUpper(strings.ReplaceAll(idemKey, "-", ""))[:12], nil
}

type paymentServer struct {
	paymentv1.UnimplementedPaymentServiceServer
	db      *gorm.DB
	cl      *clients.Clients
	gateway gateway
}

func toProto(p *Payment) *paymentv1.Payment {
	out := &paymentv1.Payment{
		Id: p.ID, UserId: p.UserID, FromAccountId: p.FromAccountID, FromAccountNumber: p.FromAccountNumber,
		BillerId: p.BillerID, BillerName: p.BillerName, CustomerReference: p.CustomerReference, Amount: p.Amount,
		Status: p.Status, FailureReason: p.FailureReason, GatewayReference: p.GatewayReference,
		FraudDecision: p.FraudDecision, LedgerEntryId: p.LedgerEntryID, IdempotencyKey: p.IdempotencyKey,
		CreatedAt: util.TS(p.CreatedAt), UpdatedAt: util.TS(p.UpdatedAt),
	}
	for _, s := range p.Steps {
		out.Steps = append(out.Steps, &paymentv1.SagaStep{Name: s.Name, Status: s.Status, Detail: s.Detail, At: util.TS(s.At)})
	}
	return out
}

func (s *paymentServer) ListBillers(ctx context.Context, _ *paymentv1.ListBillersRequest) (*paymentv1.ListBillersResponse, error) {
	var bs []Biller
	s.db.WithContext(ctx).Order("category, name").Find(&bs)
	out := &paymentv1.ListBillersResponse{}
	for _, b := range bs {
		out.Billers = append(out.Billers, &paymentv1.Biller{Id: b.ID, Name: b.Name, Category: b.Category, ReferenceLabel: b.ReferenceLabel, ReferenceHint: b.ReferenceHint})
	}
	return out, nil
}

func (s *paymentServer) CreatePayment(ctx context.Context, req *paymentv1.CreatePaymentRequest) (*paymentv1.Payment, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	key := authz.IdempotencyKey(ctx)
	if key == "" {
		key = req.IdempotencyKey
	}
	if key == "" {
		return nil, util.Invalid("Idempotency-Key header is required")
	}
	var existing Payment
	if err := s.db.WithContext(ctx).First(&existing, "user_id = ? AND idempotency_key = ?", p.UserID, key).Error; err == nil {
		if existing.Amount != req.Amount || existing.BillerID != req.BillerId {
			return nil, status.Error(codes.AlreadyExists, "idempotency key already used for a different payment")
		}
		return toProto(&existing), nil
	}
	if req.Amount <= 0 || req.Amount > 50_000_000 {
		return nil, util.Invalid("amount must be between Nu. 0.01 and Nu. 500,000")
	}
	ref := strings.TrimSpace(req.CustomerReference)
	if ref == "" {
		return nil, util.Invalid("customer reference is required")
	}
	var b Biller
	if err := s.db.WithContext(ctx).First(&b, "id = ?", req.BillerId).Error; err != nil {
		return nil, util.NotFound("biller")
	}
	acc, err := s.cl.Account().ValidateAccount(ctx, &accountv1.ValidateAccountRequest{AccountId: req.FromAccountId, OwnerUserId: p.UserID, ForDebit: true})
	if err != nil {
		return nil, err
	}
	pay := &Payment{
		ID: uuid.NewString(), UserID: p.UserID, IdempotencyKey: key, FromAccountID: acc.Id, FromAccountNumber: acc.AccountNumber,
		BillerID: b.ID, BillerName: b.Name, CustomerReference: ref, Amount: req.Amount, Status: Pending,
	}
	pay.Steps.Add("REQUEST_ACCEPTED", saga.OK, "idempotency key "+key)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(pay).Error; err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "payment.create", "payment", pay.ID, "SUCCESS", nil,
			map[string]any{"biller": b.ID, "amount": req.Amount, "from": acc.AccountNumber})
	})
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		if err := s.db.WithContext(ctx).First(&existing, "user_id = ? AND idempotency_key = ?", p.UserID, key).Error; err == nil {
			return toProto(&existing), nil
		}
	}
	if err != nil {
		return nil, util.Internal(err)
	}
	s.execute(ctx, pay)
	return toProto(pay), nil
}

func (s *paymentServer) holdRef(p *Payment) string { return "payment:" + p.ID }

func (s *paymentServer) save(ctx context.Context, p *Payment, extra func(tx *gorm.DB) error) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(p).Error; err != nil {
			return err
		}
		if extra != nil {
			return extra(tx)
		}
		return nil
	})
	if err != nil {
		slog.ErrorContext(ctx, "saving payment failed", "payment_id", p.ID, "err", err)
	}
}

func (s *paymentServer) fail(ctx context.Context, p *Payment, final, step, reason string) {
	if p.Steps.Has("FUNDS_HELD") {
		if _, err := s.cl.Account().ReleaseHold(ctx, &accountv1.ReleaseHoldRequest{Reference: s.holdRef(p)}); err == nil {
			p.Steps.Add("FUNDS_HELD", saga.Compensated, "hold released")
		}
	}
	p.Steps.Add(step, saga.Failed, reason)
	p.Status, p.FailureReason = final, reason
	paymentsTotal.WithLabelValues(final, p.BillerID).Inc()
	s.save(ctx, p, func(tx *gorm.DB) error {
		if err := events.Notify(tx, p.UserID, "PAYMENT_FAILED", map[string]string{
			"amount": util.Nu(p.Amount), "biller": p.BillerName, "reason": reason,
		}); err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "payment."+strings.ToLower(final), "payment", p.ID, "FAILURE", nil,
			map[string]any{"step": step, "reason": reason})
	})
}

func transient(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled:
		return true
	}
	return false
}

// execute runs or resumes the payment saga:
//
//	fraud score → hold funds → [review] → external gateway → ledger post → release hold
func (s *paymentServer) execute(ctx context.Context, p *Payment) {
	if p.Status != Pending {
		return
	}
	if p.FraudDecision == "" {
		res, err := s.cl.Fraud().Score(ctx, &fraudv1.ScoreRequest{
			UserId: p.UserID, AccountId: p.FromAccountID, Amount: p.Amount, Channel: "PAYMENT",
			Counterparty: "BILLER:" + p.BillerID + ":" + p.CustomerReference, ReferenceType: "PAYMENT", ReferenceId: p.ID,
		})
		if err != nil {
			s.fail(ctx, p, Failed, "FRAUD_CHECKED", "risk check unavailable, please retry")
			return
		}
		p.FraudDecision = res.Decision
		if res.Decision == "BLOCK" {
			s.fail(ctx, p, Rejected, "FRAUD_CHECKED", "blocked by risk rules ("+strings.Join(res.Reasons, "; ")+")")
			return
		}
		p.Steps.Add("FRAUD_CHECKED", saga.OK, res.Decision)
	}
	if !p.Steps.Has("FUNDS_HELD") {
		if _, err := s.cl.Account().PlaceHold(ctx, &accountv1.PlaceHoldRequest{AccountId: p.FromAccountID, Amount: p.Amount, Reference: s.holdRef(p)}); err != nil {
			if transient(err) {
				s.save(ctx, p, nil)
				return
			}
			s.fail(ctx, p, Failed, "FUNDS_HELD", status.Convert(err).Message())
			return
		}
		p.Steps.Add("FUNDS_HELD", saga.OK, util.Nu(p.Amount)+" reserved")
	}
	if p.FraudDecision == "REVIEW" {
		p.Status = PendingReview
		p.Steps.Add("FRAUD_REVIEW", saga.Waiting, "held for analyst review")
		s.save(ctx, p, func(tx *gorm.DB) error {
			return events.Notify(tx, p.UserID, "PAYMENT_UNDER_REVIEW", map[string]string{"amount": util.Nu(p.Amount), "biller": p.BillerName})
		})
		return
	}
	if p.GatewayReference == "" {
		gctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		gref, err := s.gateway.Pay(gctx, p.BillerID, p.CustomerReference, p.Amount, p.ID)
		cancel()
		if err != nil {
			s.fail(ctx, p, Failed, "GATEWAY_CONFIRMED", err.Error())
			return
		}
		p.GatewayReference = gref
		p.Steps.Add("GATEWAY_CONFIRMED", saga.OK, gref)
		s.save(ctx, p, nil)
	}
	if p.LedgerEntryID == "" {
		entry, err := s.cl.Ledger().PostEntries(ctx, &ledgerv1.PostEntriesRequest{
			IdempotencyKey: "payment:" + p.ID, Description: p.BillerName + " · " + p.CustomerReference,
			ReferenceType: "PAYMENT", ReferenceId: p.ID, Postings: ledgerx.Move(p.FromAccountID, ledgerx.PaymentClearing, p.Amount),
		})
		if err != nil {
			if transient(err) {
				s.save(ctx, p, nil)
				return
			}
			s.fail(ctx, p, Failed, "LEDGER_POSTED", status.Convert(err).Message()+" (biller payment reversed)")
			return
		}
		p.LedgerEntryID = entry.Id
		p.Steps.Add("LEDGER_POSTED", saga.OK, "entry "+entry.Id)
		if _, err := s.cl.Account().ReleaseHold(ctx, &accountv1.ReleaseHoldRequest{Reference: s.holdRef(p)}); err == nil {
			p.Steps.Add("HOLD_RELEASED", saga.OK, "")
		}
		p.Status = Success
		p.Steps.Add("COMPLETED", saga.OK, "")
		paymentsTotal.WithLabelValues(Success, p.BillerID).Inc()
		bal := ledgerx.BalanceAfter(entry, p.FromAccountID)
		s.save(ctx, p, func(tx *gorm.DB) error {
			if err := events.RecordTxn(tx, &transactionv1.RecordTransactionRequest{
				AccountId: p.FromAccountID, AccountNumber: p.FromAccountNumber, UserId: p.UserID, Type: "PAYMENT",
				Direction: ledgerx.Debit, Amount: p.Amount, BalanceAfter: bal, Counterparty: p.BillerName,
				Description: "Bill payment · " + p.CustomerReference, ReferenceType: "PAYMENT", ReferenceId: p.ID, LedgerEntryId: entry.Id,
			}); err != nil {
				return err
			}
			if err := events.Notify(tx, p.UserID, "PAYMENT_SUCCESS", map[string]string{
				"amount": util.Nu(p.Amount), "biller": p.BillerName, "reference": p.CustomerReference, "gateway_ref": p.GatewayReference,
			}); err != nil {
				return err
			}
			return events.Audit(ctx, tx, svcName, "payment.success", "payment", p.ID, "SUCCESS", nil,
				map[string]any{"gateway_reference": p.GatewayReference, "ledger_entry_id": entry.Id})
		})
	}
}

func (s *paymentServer) ResolveReview(ctx context.Context, req *paymentv1.ResolveReviewRequest) (*paymentv1.Payment, error) {
	var p Payment
	if err := s.db.WithContext(ctx).First(&p, "id = ?", req.PaymentId).Error; err != nil {
		return nil, util.NotFound("payment")
	}
	if p.Status != PendingReview {
		return toProto(&p), nil
	}
	if !req.Approve {
		s.fail(ctx, &p, Rejected, "FRAUD_REVIEW", "rejected by fraud analyst")
		return toProto(&p), nil
	}
	p.Status, p.FraudDecision = Pending, "ALLOW"
	p.Steps.Add("FRAUD_REVIEW", saga.OK, "approved by analyst")
	s.save(ctx, &p, nil)
	s.execute(ctx, &p)
	return toProto(&p), nil
}

func (s *paymentServer) recover(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			var stuck []Payment
			s.db.WithContext(ctx).Where("status = ? AND updated_at < ?", Pending, time.Now().UTC().Add(-15*time.Second)).Limit(20).Find(&stuck)
			for i := range stuck {
				sctx := authz.SystemContext(ctx)
				if time.Since(stuck[i].CreatedAt) > time.Hour {
					s.fail(sctx, &stuck[i], Failed, "RECOVERY", "timed out")
					continue
				}
				s.execute(sctx, &stuck[i])
			}
		}
	}
}

func (s *paymentServer) GetPayment(ctx context.Context, req *paymentv1.GetPaymentRequest) (*paymentv1.Payment, error) {
	pr, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	var p Payment
	if err := s.db.WithContext(ctx).First(&p, "id = ?", req.PaymentId).Error; err != nil || !pr.CanAccessUser(p.UserID) {
		return nil, util.NotFound("payment")
	}
	return toProto(&p), nil
}

func (s *paymentServer) ListPayments(ctx context.Context, req *paymentv1.ListPaymentsRequest) (*paymentv1.ListPaymentsResponse, error) {
	pr, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	limit, offset := util.Page(req.PageSize, req.PageToken)
	var ps []Payment
	if err := s.db.WithContext(ctx).Where("user_id = ?", pr.UserID).Order("created_at DESC").Limit(limit).Offset(offset).Find(&ps).Error; err != nil {
		return nil, util.Internal(err)
	}
	out := &paymentv1.ListPaymentsResponse{NextPageToken: util.NextToken(len(ps), limit, offset)}
	for i := range ps {
		out.Payments = append(out.Payments, toProto(&ps[i]))
	}
	return out, nil
}
