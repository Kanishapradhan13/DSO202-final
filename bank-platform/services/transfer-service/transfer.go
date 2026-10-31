package main

import (
	"context"
	"errors"
	"log/slog"
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
	transactionv1 "github.com/dso202/bank-platform/gen/go/bank/transaction/v1"
	transferv1 "github.com/dso202/bank-platform/gen/go/bank/transfer/v1"
	"github.com/dso202/bank-platform/pkg/authz"
	"github.com/dso202/bank-platform/pkg/clients"
	"github.com/dso202/bank-platform/pkg/events"
	"github.com/dso202/bank-platform/pkg/ledgerx"
	"github.com/dso202/bank-platform/pkg/registry"
	"github.com/dso202/bank-platform/pkg/saga"
	"github.com/dso202/bank-platform/pkg/util"
)

const svcName = "transfer-service"

// Transfer statuses.
const (
	Pending       = "PENDING"
	PendingReview = "PENDING_REVIEW"
	Completed     = "COMPLETED"
	Failed        = "FAILED"
	Rejected      = "REJECTED"
)

const maxTransfer = 100_000_000 // Nu. 1,000,000 per transfer

var (
	transfersTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "bank_transfers_total", Help: "Transfers by final status.",
	}, []string{"status"})
	transferValue = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bank_transfer_value_chhertrum_total", Help: "Value of completed transfers, in chhertrum.",
	})
	sagaDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name: "bank_transfer_saga_seconds", Help: "Time to run the transfer saga.",
		Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5},
	})
)

// Transfer is one transfer order and its saga state.
type Transfer struct {
	ID                string `gorm:"primaryKey;type:uuid"`
	UserID            string `gorm:"type:uuid;not null;uniqueIndex:idx_transfer_idem;index"`
	IdempotencyKey    string `gorm:"not null;uniqueIndex:idx_transfer_idem"`
	FromAccountID     string `gorm:"type:uuid;index;not null"`
	FromAccountNumber string
	ToAccountID       string `gorm:"type:uuid"`
	ToAccountNumber   string
	ToName            string
	ToUserID          string
	Amount            int64  `gorm:"not null"`
	Description       string
	Status            string `gorm:"index;not null"`
	FailureReason     string
	FraudDecision     string
	FraudScore        int32
	FraudAlertID      string
	LedgerEntryID     string
	Steps             saga.Steps
	CreatedAt         time.Time `gorm:"index"`
	UpdatedAt         time.Time
}

// Payee is a saved beneficiary.
type Payee struct {
	ID            string `gorm:"primaryKey;type:uuid"`
	UserID        string `gorm:"type:uuid;not null;uniqueIndex:idx_payee_user_acct"`
	Nickname      string `gorm:"not null"`
	AccountNumber string `gorm:"not null;uniqueIndex:idx_payee_user_acct"`
	HolderName    string
	CreatedAt     time.Time
}

type transferServer struct {
	transferv1.UnimplementedTransferServiceServer
	db       *gorm.DB
	cl       *clients.Clients
	dailyCap *registry.IntSetting
}

func toProto(t *Transfer) *transferv1.Transfer {
	out := &transferv1.Transfer{
		Id: t.ID, UserId: t.UserID, FromAccountId: t.FromAccountID, FromAccountNumber: t.FromAccountNumber,
		ToAccountId: t.ToAccountID, ToAccountNumber: t.ToAccountNumber, ToName: t.ToName, Amount: t.Amount,
		Description: t.Description, Status: t.Status, FailureReason: t.FailureReason, FraudDecision: t.FraudDecision,
		FraudScore: t.FraudScore, LedgerEntryId: t.LedgerEntryID, IdempotencyKey: t.IdempotencyKey,
		CreatedAt: util.TS(t.CreatedAt), UpdatedAt: util.TS(t.UpdatedAt),
	}
	for _, s := range t.Steps {
		out.Steps = append(out.Steps, &transferv1.SagaStep{Name: s.Name, Status: s.Status, Detail: s.Detail, At: util.TS(s.At)})
	}
	return out
}

func (s *transferServer) CreateTransfer(ctx context.Context, req *transferv1.CreateTransferRequest) (*transferv1.Transfer, error) {
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
	// Same key, same result.
	var existing Transfer
	if err := s.db.WithContext(ctx).First(&existing, "user_id = ? AND idempotency_key = ?", p.UserID, key).Error; err == nil {
		if existing.Amount != req.Amount || existing.FromAccountID != req.FromAccountId || existing.ToAccountNumber != strings.TrimSpace(req.ToAccountNumber) {
			return nil, status.Error(codes.AlreadyExists, "idempotency key already used for a different transfer")
		}
		return toProto(&existing), nil
	}
	if req.Amount <= 0 {
		return nil, util.Invalid("amount must be positive")
	}
	if req.Amount > maxTransfer {
		return nil, util.Invalid("amount exceeds the per-transfer maximum of %s", util.Nu(maxTransfer))
	}
	from, err := s.cl.Account().ValidateAccount(ctx, &accountv1.ValidateAccountRequest{AccountId: req.FromAccountId, OwnerUserId: p.UserID, ForDebit: true})
	if err != nil {
		return nil, err
	}
	toNum := strings.TrimSpace(req.ToAccountNumber)
	toRef, err := s.cl.Account().ResolveAccountNumber(ctx, &accountv1.ResolveAccountNumberRequest{AccountNumber: toNum})
	if err != nil {
		return nil, util.Invalid("beneficiary account %s not found", toNum)
	}
	to, err := s.cl.Account().ValidateAccount(ctx, &accountv1.ValidateAccountRequest{AccountId: toRef.Id})
	if err != nil {
		return nil, err
	}
	if from.Id == to.Id {
		return nil, util.Invalid("cannot transfer to the same account")
	}
	desc := strings.TrimSpace(req.Description)
	if len(desc) > 140 {
		desc = desc[:140]
	}
	t := &Transfer{
		ID: uuid.NewString(), UserID: p.UserID, IdempotencyKey: key, FromAccountID: from.Id, FromAccountNumber: from.AccountNumber,
		ToAccountID: to.Id, ToAccountNumber: to.AccountNumber, ToName: to.HolderName, ToUserID: to.UserId,
		Amount: req.Amount, Description: desc, Status: Pending,
	}
	t.Steps.Add("REQUEST_ACCEPTED", saga.OK, "idempotency key "+key)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(t).Error; err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "transfer.create", "transfer", t.ID, "SUCCESS", nil,
			map[string]any{"from": from.AccountNumber, "to": to.AccountNumber, "amount": req.Amount})
	})
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		if err := s.db.WithContext(ctx).First(&existing, "user_id = ? AND idempotency_key = ?", p.UserID, key).Error; err == nil {
			return toProto(&existing), nil
		}
	}
	if err != nil {
		return nil, util.Internal(err)
	}
	start := time.Now()
	s.execute(ctx, t)
	sagaDuration.Observe(time.Since(start).Seconds())
	return toProto(t), nil
}

func (s *transferServer) save(ctx context.Context, t *Transfer, extra func(tx *gorm.DB) error) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(t).Error; err != nil {
			return err
		}
		if extra != nil {
			return extra(tx)
		}
		return nil
	})
	if err != nil {
		slog.ErrorContext(ctx, "saving transfer failed", "transfer_id", t.ID, "err", err)
	}
}

func transient(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled, codes.ResourceExhausted:
		return true
	}
	return false
}

func (s *transferServer) holdRef(t *Transfer) string { return "transfer:" + t.ID }

// fail ends the saga, compensating (releasing the hold) if funds were reserved.
func (s *transferServer) fail(ctx context.Context, t *Transfer, finalStatus, step, reason string) {
	if t.Steps.Has("FUNDS_HELD") {
		if _, err := s.cl.Account().ReleaseHold(ctx, &accountv1.ReleaseHoldRequest{Reference: s.holdRef(t)}); err == nil {
			t.Steps.Add("FUNDS_HELD", saga.Compensated, "hold released")
		}
	}
	t.Steps.Add(step, saga.Failed, reason)
	t.Status, t.FailureReason = finalStatus, reason
	transfersTotal.WithLabelValues(finalStatus).Inc()
	s.save(ctx, t, func(tx *gorm.DB) error {
		tmpl := "TRANSFER_FAILED"
		if finalStatus == Rejected {
			tmpl = "TRANSFER_REJECTED"
		}
		if err := events.Notify(tx, t.UserID, tmpl, map[string]string{
			"amount": util.Nu(t.Amount), "to": t.ToAccountNumber, "reason": reason,
		}); err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "transfer."+strings.ToLower(finalStatus), "transfer", t.ID, "FAILURE", nil,
			map[string]any{"step": step, "reason": reason})
	})
}

// execute runs (or resumes) the saga. Every step is idempotent, so a crashed
// or retried saga can run execute again safely:
//
//	limit check → fraud score → hold funds → [review] → ledger post → release hold → complete
func (s *transferServer) execute(ctx context.Context, t *Transfer) {
	if t.Status != Pending {
		return
	}
	// 1. Daily limit.
	if !t.Steps.Has("LIMIT_CHECKED") {
		acc, err := s.cl.Account().ValidateAccount(ctx, &accountv1.ValidateAccountRequest{AccountId: t.FromAccountID, ForDebit: true})
		if err != nil {
			if transient(err) {
				return
			}
			s.fail(ctx, t, Failed, "LIMIT_CHECKED", status.Convert(err).Message())
			return
		}
		limit := acc.DailyTransferLimit
		if c := s.dailyCap.Get(); c > 0 && (limit == 0 || c < limit) {
			limit = c
		}
		var used int64
		s.db.WithContext(ctx).Model(&Transfer{}).
			Where("from_account_id = ? AND id <> ? AND created_at >= ? AND status IN ?", t.FromAccountID, t.ID,
				util.StartOfDay(time.Now()), []string{Pending, PendingReview, Completed}).
			Select("COALESCE(SUM(amount), 0)").Scan(&used)
		if used+t.Amount > limit {
			s.fail(ctx, t, Failed, "LIMIT_CHECKED", "daily transfer limit of "+util.Nu(limit)+" exceeded (used "+util.Nu(used)+")")
			return
		}
		t.Steps.Add("LIMIT_CHECKED", saga.OK, "used "+util.Nu(used)+" of "+util.Nu(limit))
	}
	// 2. Fraud pre-check (synchronous on purpose: no score, no transfer).
	if t.FraudDecision == "" {
		var newPayee int64
		s.db.WithContext(ctx).Model(&Transfer{}).Where("user_id = ? AND to_account_id = ? AND status = ?", t.UserID, t.ToAccountID, Completed).Count(&newPayee)
		counterparty := t.ToAccountNumber
		if newPayee == 0 {
			counterparty = "NEW:" + counterparty
		}
		res, err := s.cl.Fraud().Score(ctx, &fraudv1.ScoreRequest{
			UserId: t.UserID, AccountId: t.FromAccountID, Amount: t.Amount, Channel: "TRANSFER",
			Counterparty: counterparty, ReferenceType: "TRANSFER", ReferenceId: t.ID,
		})
		if err != nil {
			// Fail closed: without a risk score the money does not move.
			s.fail(ctx, t, Failed, "FRAUD_CHECKED", "risk check unavailable, please retry")
			return
		}
		t.FraudDecision, t.FraudScore, t.FraudAlertID = res.Decision, res.Score, res.AlertId
		detail := res.Decision
		if len(res.Reasons) > 0 {
			detail += ": " + strings.Join(res.Reasons, "; ")
		}
		if res.Decision == "BLOCK" {
			s.fail(ctx, t, Rejected, "FRAUD_CHECKED", "blocked by risk rules ("+strings.Join(res.Reasons, "; ")+")")
			return
		}
		t.Steps.Add("FRAUD_CHECKED", saga.OK, detail)
		s.save(ctx, t, nil)
	}
	// 3. Reserve funds.
	if !t.Steps.Has("FUNDS_HELD") {
		if _, err := s.cl.Account().PlaceHold(ctx, &accountv1.PlaceHoldRequest{AccountId: t.FromAccountID, Amount: t.Amount, Reference: s.holdRef(t)}); err != nil {
			if transient(err) {
				s.save(ctx, t, nil)
				return
			}
			s.fail(ctx, t, Failed, "FUNDS_HELD", status.Convert(err).Message())
			return
		}
		t.Steps.Add("FUNDS_HELD", saga.OK, util.Nu(t.Amount)+" reserved")
	}
	// 3b. Manual review parks the saga with the funds held.
	if t.FraudDecision == "REVIEW" {
		t.Status = PendingReview
		t.Steps.Add("FRAUD_REVIEW", saga.Waiting, "held for analyst review")
		s.save(ctx, t, func(tx *gorm.DB) error {
			return events.Notify(tx, t.UserID, "TRANSFER_UNDER_REVIEW", map[string]string{"amount": util.Nu(t.Amount), "to": t.ToAccountNumber})
		})
		return
	}
	// 4. Post to the ledger: debit and credit in one DB transaction, summing to zero.
	if t.LedgerEntryID == "" {
		desc := "Transfer to " + t.ToAccountNumber
		if t.Description != "" {
			desc += ": " + t.Description
		}
		entry, err := s.cl.Ledger().PostEntries(ctx, &ledgerv1.PostEntriesRequest{
			IdempotencyKey: "transfer:" + t.ID, Description: desc, ReferenceType: "TRANSFER", ReferenceId: t.ID,
			Postings: ledgerx.Move(t.FromAccountID, t.ToAccountID, t.Amount),
		})
		if err != nil {
			if transient(err) {
				// Unknown outcome; the recovery worker retries with the same key.
				s.save(ctx, t, nil)
				return
			}
			s.fail(ctx, t, Failed, "LEDGER_POSTED", status.Convert(err).Message())
			return
		}
		t.LedgerEntryID = entry.Id
		t.Steps.Add("LEDGER_POSTED", saga.OK, "entry "+entry.Id)
		s.complete(ctx, t, entry)
	}
}

func (s *transferServer) complete(ctx context.Context, t *Transfer, entry *ledgerv1.JournalEntry) {
	if _, err := s.cl.Account().ReleaseHold(ctx, &accountv1.ReleaseHoldRequest{Reference: s.holdRef(t)}); err == nil {
		t.Steps.Add("HOLD_RELEASED", saga.OK, "")
	} else {
		t.Steps.Add("HOLD_RELEASED", saga.Failed, "will expire automatically")
	}
	t.Status = Completed
	t.Steps.Add("COMPLETED", saga.OK, "")
	transfersTotal.WithLabelValues(Completed).Inc()
	transferValue.Add(float64(t.Amount))
	fromBal, toBal := ledgerx.BalanceAfter(entry, t.FromAccountID), ledgerx.BalanceAfter(entry, t.ToAccountID)
	senderName := authz.FromContext(ctx).Name
	s.save(ctx, t, func(tx *gorm.DB) error {
		desc := t.Description
		if desc == "" {
			desc = "Fund transfer"
		}
		if err := events.RecordTxn(tx, &transactionv1.RecordTransactionRequest{
			AccountId: t.FromAccountID, AccountNumber: t.FromAccountNumber, UserId: t.UserID, Type: "TRANSFER_OUT",
			Direction: ledgerx.Debit, Amount: t.Amount, BalanceAfter: fromBal, Counterparty: t.ToName + " · " + t.ToAccountNumber,
			Description: desc, ReferenceType: "TRANSFER", ReferenceId: t.ID, LedgerEntryId: t.LedgerEntryID,
		}); err != nil {
			return err
		}
		if err := events.RecordTxn(tx, &transactionv1.RecordTransactionRequest{
			AccountId: t.ToAccountID, AccountNumber: t.ToAccountNumber, UserId: t.ToUserID, Type: "TRANSFER_IN",
			Direction: ledgerx.Credit, Amount: t.Amount, BalanceAfter: toBal, Counterparty: t.FromAccountNumber,
			Description: desc, ReferenceType: "TRANSFER", ReferenceId: t.ID, LedgerEntryId: t.LedgerEntryID,
		}); err != nil {
			return err
		}
		if err := events.Notify(tx, t.UserID, "TRANSFER_SENT", map[string]string{
			"amount": util.Nu(t.Amount), "to": t.ToName + " (" + t.ToAccountNumber + ")", "balance": util.Nu(fromBal),
		}); err != nil {
			return err
		}
		if err := events.Notify(tx, t.ToUserID, "TRANSFER_RECEIVED", map[string]string{
			"amount": util.Nu(t.Amount), "from": strings.TrimSpace(senderName + " " + t.FromAccountNumber), "account_number": t.ToAccountNumber,
		}); err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "transfer.completed", "transfer", t.ID, "SUCCESS",
			map[string]any{"status": Pending}, map[string]any{"status": Completed, "ledger_entry_id": t.LedgerEntryID})
	})
}

// ResolveReview continues or cancels a transfer after an analyst decision.
func (s *transferServer) ResolveReview(ctx context.Context, req *transferv1.ResolveReviewRequest) (*transferv1.Transfer, error) {
	var t Transfer
	if err := s.db.WithContext(ctx).First(&t, "id = ?", req.TransferId).Error; err != nil {
		return nil, util.NotFound("transfer")
	}
	if t.Status != PendingReview {
		return toProto(&t), nil
	}
	if !req.Approve {
		s.fail(ctx, &t, Rejected, "FRAUD_REVIEW", "rejected by fraud analyst"+noteSuffix(req.Note))
		return toProto(&t), nil
	}
	t.Status, t.FraudDecision = Pending, "ALLOW"
	t.Steps.Add("FRAUD_REVIEW", saga.OK, "approved by analyst"+noteSuffix(req.Note))
	s.save(ctx, &t, nil)
	s.execute(ctx, &t)
	return toProto(&t), nil
}

func noteSuffix(n string) string {
	if n = strings.TrimSpace(n); n != "" {
		return ": " + n
	}
	return ""
}

// recover resumes sagas left PENDING by a crash or a transient failure.
func (s *transferServer) recover(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			var stuck []Transfer
			s.db.WithContext(ctx).Where("status = ? AND updated_at < ?", Pending, time.Now().UTC().Add(-15*time.Second)).
				Limit(20).Find(&stuck)
			for i := range stuck {
				slog.Info("resuming transfer saga", "transfer_id", stuck[i].ID)
				if time.Since(stuck[i].CreatedAt) > time.Hour {
					s.fail(authz.SystemContext(ctx), &stuck[i], Failed, "RECOVERY", "timed out")
					continue
				}
				s.execute(authz.SystemContext(ctx), &stuck[i])
			}
		}
	}
}

func (s *transferServer) GetTransfer(ctx context.Context, req *transferv1.GetTransferRequest) (*transferv1.Transfer, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	var t Transfer
	if err := s.db.WithContext(ctx).First(&t, "id = ?", req.TransferId).Error; err != nil || !p.CanAccessUser(t.UserID) {
		return nil, util.NotFound("transfer")
	}
	return toProto(&t), nil
}

func (s *transferServer) ListTransfers(ctx context.Context, req *transferv1.ListTransfersRequest) (*transferv1.ListTransfersResponse, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	limit, offset := util.Page(req.PageSize, req.PageToken)
	q := s.db.WithContext(ctx).Where("user_id = ?", p.UserID)
	if req.Status != "" {
		q = q.Where("status = ?", req.Status)
	}
	var ts []Transfer
	if err := q.Order("created_at DESC").Limit(limit).Offset(offset).Find(&ts).Error; err != nil {
		return nil, util.Internal(err)
	}
	out := &transferv1.ListTransfersResponse{NextPageToken: util.NextToken(len(ts), limit, offset)}
	for i := range ts {
		out.Transfers = append(out.Transfers, toProto(&ts[i]))
	}
	return out, nil
}

func payeeProto(p *Payee) *transferv1.Payee {
	return &transferv1.Payee{Id: p.ID, Nickname: p.Nickname, AccountNumber: p.AccountNumber, HolderName: p.HolderName, CreatedAt: util.TS(p.CreatedAt)}
}

func (s *transferServer) AddPayee(ctx context.Context, req *transferv1.AddPayeeRequest) (*transferv1.Payee, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	num := strings.TrimSpace(req.AccountNumber)
	look, err := s.cl.Account().LookupAccount(ctx, &accountv1.LookupAccountRequest{AccountNumber: num})
	if err != nil {
		return nil, util.Invalid("account %s not found", num)
	}
	nick := strings.TrimSpace(req.Nickname)
	if nick == "" {
		nick = look.HolderName
	}
	py := Payee{ID: uuid.NewString(), UserID: p.UserID, Nickname: nick, AccountNumber: look.AccountNumber, HolderName: look.HolderName}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&py).Error; err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "payee.add", "payee", py.ID, "SUCCESS", nil, map[string]any{"account_number": py.AccountNumber})
	})
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return nil, status.Error(codes.AlreadyExists, "payee already saved")
	}
	if err != nil {
		return nil, util.Internal(err)
	}
	return payeeProto(&py), nil
}

func (s *transferServer) ListPayees(ctx context.Context, _ *transferv1.ListPayeesRequest) (*transferv1.ListPayeesResponse, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	var ps []Payee
	s.db.WithContext(ctx).Where("user_id = ?", p.UserID).Order("nickname").Find(&ps)
	out := &transferv1.ListPayeesResponse{}
	for i := range ps {
		out.Payees = append(out.Payees, payeeProto(&ps[i]))
	}
	return out, nil
}

func (s *transferServer) DeletePayee(ctx context.Context, req *transferv1.DeletePayeeRequest) (*transferv1.DeletePayeeResponse, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Where("id = ? AND user_id = ?", req.PayeeId, p.UserID).Delete(&Payee{})
		if res.RowsAffected == 0 {
			return util.NotFound("payee")
		}
		return events.Audit(ctx, tx, svcName, "payee.delete", "payee", req.PayeeId, "SUCCESS", nil, nil)
	})
	if err != nil {
		return nil, err
	}
	return &transferv1.DeletePayeeResponse{}, nil
}
