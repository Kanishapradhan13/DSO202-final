// transaction-service handles teller cash deposits and withdrawals and keeps the
// transaction-history read model for every money movement.
package main

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	accountv1 "github.com/dso202/bank-platform/gen/go/bank/account/v1"
	fraudv1 "github.com/dso202/bank-platform/gen/go/bank/fraud/v1"
	ledgerv1 "github.com/dso202/bank-platform/gen/go/bank/ledger/v1"
	transactionv1 "github.com/dso202/bank-platform/gen/go/bank/transaction/v1"
	"github.com/dso202/bank-platform/pkg/authz"
	"github.com/dso202/bank-platform/pkg/clients"
	"github.com/dso202/bank-platform/pkg/config"
	"github.com/dso202/bank-platform/pkg/db"
	"github.com/dso202/bank-platform/pkg/events"
	"github.com/dso202/bank-platform/pkg/ledgerx"
	"github.com/dso202/bank-platform/pkg/server"
	"github.com/dso202/bank-platform/pkg/util"
)

const svcName = "transaction-service"

// Transaction is one line of account history (a read model of ledger postings).
type Transaction struct {
	ID            string `gorm:"primaryKey;type:uuid"`
	AccountID     string `gorm:"type:uuid;not null;index:idx_txn_account_time;uniqueIndex:idx_txn_ref"`
	AccountNumber string `gorm:"index"`
	UserID        string `gorm:"type:uuid;index"`
	Type          string `gorm:"index;not null"`
	Direction     string `gorm:"not null;uniqueIndex:idx_txn_ref"`
	Amount        int64  `gorm:"not null"`
	BalanceAfter  int64
	Counterparty  string
	Description   string
	ReferenceType string `gorm:"uniqueIndex:idx_txn_ref"`
	ReferenceID   string `gorm:"uniqueIndex:idx_txn_ref"`
	LedgerEntryID string
	Status        string
	CreatedAt     time.Time `gorm:"index:idx_txn_account_time"`
}

type txnServer struct {
	transactionv1.UnimplementedTransactionServiceServer
	db         *gorm.DB
	cl         *clients.Clients
	lowBalance int64
}

func main() {
	cfg := server.Init(svcName)
	gdb := db.MustOpen(cfg, &Transaction{})
	if cfg.MigrateOnly {
		return
	}
	cl := clients.New(svcName)
	defer cl.Close()
	svc := &txnServer{db: gdb, cl: cl, lowBalance: config.Int("LOW_BALANCE_THRESHOLD", 100_000)}
	server.Run(server.Service{
		Config:   cfg,
		Register: func(s *grpc.Server) { transactionv1.RegisterTransactionServiceServer(s, svc) },
		Workers:  []server.Worker{events.Dispatcher(gdb, cl).Run},
	})
}

func toProto(t *Transaction) *transactionv1.Transaction {
	return &transactionv1.Transaction{
		Id: t.ID, AccountId: t.AccountID, AccountNumber: t.AccountNumber, UserId: t.UserID, Type: t.Type,
		Direction: t.Direction, Amount: t.Amount, BalanceAfter: t.BalanceAfter, Counterparty: t.Counterparty,
		Description: t.Description, ReferenceType: t.ReferenceType, ReferenceId: t.ReferenceID,
		LedgerEntryId: t.LedgerEntryID, Status: t.Status, CreatedAt: util.TS(t.CreatedAt),
	}
}

// record inserts a history line; a duplicate (same account, reference and direction) is ignored.
func (s *txnServer) record(tx *gorm.DB, t *Transaction) error {
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	if t.Status == "" {
		t.Status = "COMPLETED"
	}
	res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(t)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 1 && t.Direction == ledgerx.Debit && t.BalanceAfter < s.lowBalance && t.UserID != "" {
		return events.Notify(tx, t.UserID, "LOW_BALANCE", map[string]string{
			"account_number": t.AccountNumber, "balance": util.Nu(t.BalanceAfter),
		})
	}
	return nil
}

func (s *txnServer) RecordTransaction(ctx context.Context, req *transactionv1.RecordTransactionRequest) (*transactionv1.Transaction, error) {
	if req.AccountId == "" || req.Amount <= 0 || req.ReferenceId == "" {
		return nil, util.Invalid("account_id, amount and reference_id are required")
	}
	t := &Transaction{
		AccountID: req.AccountId, AccountNumber: req.AccountNumber, UserID: req.UserId, Type: req.Type, Direction: req.Direction,
		Amount: req.Amount, BalanceAfter: req.BalanceAfter, Counterparty: req.Counterparty, Description: req.Description,
		ReferenceType: req.ReferenceType, ReferenceID: req.ReferenceId, LedgerEntryID: req.LedgerEntryId,
	}
	if req.OccurredAt != nil {
		t.CreatedAt = req.OccurredAt.AsTime()
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return s.record(tx, t) }); err != nil {
		return nil, util.Internal(err)
	}
	return toProto(t), nil
}

func (s *txnServer) resolve(ctx context.Context, req *transactionv1.CashRequest, forDebit bool) (*accountv1.Account, error) {
	id := req.AccountId
	if id == "" && req.AccountNumber != "" {
		a, err := s.cl.Account().ResolveAccountNumber(ctx, &accountv1.ResolveAccountNumberRequest{AccountNumber: req.AccountNumber})
		if err != nil {
			return nil, err
		}
		id = a.Id
	}
	if id == "" {
		return nil, util.Invalid("account_id or account_number is required")
	}
	return s.cl.Account().ValidateAccount(ctx, &accountv1.ValidateAccountRequest{AccountId: id, ForDebit: forDebit})
}

func idemKey(ctx context.Context) (string, error) {
	k := authz.IdempotencyKey(ctx)
	if k == "" {
		return "", util.Invalid("Idempotency-Key header is required")
	}
	return k, nil
}

// Deposit credits cash paid in at a branch (teller operation).
func (s *txnServer) Deposit(ctx context.Context, req *transactionv1.CashRequest) (*transactionv1.Transaction, error) {
	p, err := authz.RequireRole(ctx, authz.RoleOpsOfficer)
	if err != nil {
		return nil, err
	}
	if req.Amount <= 0 || req.Amount > 100_000_000 {
		return nil, util.Invalid("amount must be between Nu. 0.01 and Nu. 1,000,000")
	}
	key, err := idemKey(ctx)
	if err != nil {
		return nil, err
	}
	acc, err := s.resolve(ctx, req, false)
	if err != nil {
		return nil, err
	}
	ref := "deposit:" + key
	desc := strings.TrimSpace(req.Description)
	if desc == "" {
		desc = "Cash deposit"
	}
	entry, err := s.cl.Ledger().PostEntries(ctx, &ledgerv1.PostEntriesRequest{
		IdempotencyKey: ref, Description: desc, ReferenceType: "DEPOSIT", ReferenceId: key,
		Postings: ledgerx.Move(ledgerx.Cash, acc.Id, req.Amount),
	})
	if err != nil {
		return nil, err
	}
	t := &Transaction{
		AccountID: acc.Id, AccountNumber: acc.AccountNumber, UserID: acc.UserId, Type: "DEPOSIT", Direction: ledgerx.Credit,
		Amount: req.Amount, BalanceAfter: ledgerx.BalanceAfter(entry, acc.Id), Counterparty: "Branch teller",
		Description: desc, ReferenceType: "DEPOSIT", ReferenceID: key, LedgerEntryID: entry.Id,
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.record(tx, t); err != nil {
			return err
		}
		if err := events.Notify(tx, acc.UserId, "DEPOSIT_RECEIVED", map[string]string{
			"amount": util.Nu(req.Amount), "account_number": acc.AccountNumber, "balance": util.Nu(t.BalanceAfter),
		}); err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "cash.deposit", "account", acc.Id, "SUCCESS", nil,
			map[string]any{"amount": req.Amount, "teller": p.UserID, "ledger_entry_id": entry.Id})
	})
	if err != nil {
		return nil, util.Internal(err)
	}
	return s.byRef(ctx, acc.Id, "DEPOSIT", key, ledgerx.Credit, t)
}

// Withdraw debits cash paid out at a branch, after a fraud check and a hold.
func (s *txnServer) Withdraw(ctx context.Context, req *transactionv1.CashRequest) (*transactionv1.Transaction, error) {
	p, err := authz.RequireRole(ctx, authz.RoleOpsOfficer)
	if err != nil {
		return nil, err
	}
	if req.Amount <= 0 {
		return nil, util.Invalid("amount must be positive")
	}
	key, err := idemKey(ctx)
	if err != nil {
		return nil, err
	}
	acc, err := s.resolve(ctx, req, true)
	if err != nil {
		return nil, err
	}
	var existing Transaction
	if err := s.db.WithContext(ctx).First(&existing, "reference_type = ? AND reference_id = ?", "WITHDRAWAL", key).Error; err == nil {
		return toProto(&existing), nil
	}
	score, err := s.cl.Fraud().Score(ctx, &fraudv1.ScoreRequest{
		UserId: acc.UserId, AccountId: acc.Id, Amount: req.Amount, Channel: "WITHDRAWAL",
		Counterparty: "BRANCH", ReferenceType: "WITHDRAWAL", ReferenceId: key,
	})
	if err != nil {
		return nil, status.Error(codes.Unavailable, "fraud check unavailable; withdrawal not processed")
	}
	if score.Decision == "BLOCK" {
		return nil, status.Errorf(codes.PermissionDenied, "withdrawal blocked by risk rules: %s", strings.Join(score.Reasons, "; "))
	}
	ref := "withdrawal:" + key
	if _, err := s.cl.Account().PlaceHold(ctx, &accountv1.PlaceHoldRequest{AccountId: acc.Id, Amount: req.Amount, Reference: ref}); err != nil {
		return nil, err
	}
	desc := strings.TrimSpace(req.Description)
	if desc == "" {
		desc = "Cash withdrawal"
	}
	entry, err := s.cl.Ledger().PostEntries(ctx, &ledgerv1.PostEntriesRequest{
		IdempotencyKey: ref, Description: desc, ReferenceType: "WITHDRAWAL", ReferenceId: key,
		Postings: ledgerx.Move(acc.Id, ledgerx.Cash, req.Amount),
	})
	_, _ = s.cl.Account().ReleaseHold(ctx, &accountv1.ReleaseHoldRequest{Reference: ref})
	if err != nil {
		return nil, err
	}
	t := &Transaction{
		AccountID: acc.Id, AccountNumber: acc.AccountNumber, UserID: acc.UserId, Type: "WITHDRAWAL", Direction: ledgerx.Debit,
		Amount: req.Amount, BalanceAfter: ledgerx.BalanceAfter(entry, acc.Id), Counterparty: "Branch teller",
		Description: desc, ReferenceType: "WITHDRAWAL", ReferenceID: key, LedgerEntryID: entry.Id,
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.record(tx, t); err != nil {
			return err
		}
		if err := events.Notify(tx, acc.UserId, "WITHDRAWAL_MADE", map[string]string{
			"amount": util.Nu(req.Amount), "account_number": acc.AccountNumber, "balance": util.Nu(t.BalanceAfter),
		}); err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "cash.withdraw", "account", acc.Id, "SUCCESS", nil,
			map[string]any{"amount": req.Amount, "teller": p.UserID, "ledger_entry_id": entry.Id, "fraud_decision": score.Decision})
	})
	if err != nil {
		return nil, util.Internal(err)
	}
	return s.byRef(ctx, acc.Id, "WITHDRAWAL", key, ledgerx.Debit, t)
}

func (s *txnServer) byRef(ctx context.Context, accountID, refType, refID, dir string, fallback *Transaction) (*transactionv1.Transaction, error) {
	var t Transaction
	if err := s.db.WithContext(ctx).First(&t, "account_id = ? AND reference_type = ? AND reference_id = ? AND direction = ?",
		accountID, refType, refID, dir).Error; err != nil {
		return toProto(fallback), nil
	}
	return toProto(&t), nil
}

func applyFilters(q *gorm.DB, typ, dir, from, to string, minA, maxA int64, query string) (*gorm.DB, error) {
	if typ != "" {
		q = q.Where("type = ?", typ)
	}
	if dir != "" {
		q = q.Where("direction = ?", dir)
	}
	if from != "" {
		t, err := util.ParseDate(from)
		if err != nil {
			return nil, util.Invalid("from must be YYYY-MM-DD")
		}
		q = q.Where("created_at >= ?", t)
	}
	if to != "" {
		t, err := util.ParseDate(to)
		if err != nil {
			return nil, util.Invalid("to must be YYYY-MM-DD")
		}
		q = q.Where("created_at < ?", t.AddDate(0, 0, 1))
	}
	if minA > 0 {
		q = q.Where("amount >= ?", minA)
	}
	if maxA > 0 {
		q = q.Where("amount <= ?", maxA)
	}
	if v := strings.TrimSpace(query); v != "" {
		like := "%" + strings.ToLower(v) + "%"
		q = q.Where("LOWER(description) LIKE ? OR LOWER(counterparty) LIKE ?", like, like)
	}
	return q, nil
}

func (s *txnServer) page(q *gorm.DB, size int32, token string) (*transactionv1.ListTransactionsResponse, error) {
	limit, offset := util.Page(size, token)
	var ts []Transaction
	if err := q.Order("created_at DESC, id").Limit(limit).Offset(offset).Find(&ts).Error; err != nil {
		return nil, util.Internal(err)
	}
	out := &transactionv1.ListTransactionsResponse{NextPageToken: util.NextToken(len(ts), limit, offset)}
	for i := range ts {
		out.Transactions = append(out.Transactions, toProto(&ts[i]))
	}
	return out, nil
}

func (s *txnServer) ListTransactions(ctx context.Context, req *transactionv1.ListTransactionsRequest) (*transactionv1.ListTransactionsResponse, error) {
	if _, err := authz.RequireUser(ctx); err != nil {
		return nil, err
	}
	// account-service enforces that the caller owns the account (or is staff).
	if _, err := s.cl.Account().GetAccount(ctx, &accountv1.GetAccountRequest{AccountId: req.AccountId}); err != nil {
		if status.Code(err) == codes.Unavailable {
			return nil, err
		}
		return nil, util.NotFound("account")
	}
	q, err := applyFilters(s.db.WithContext(ctx).Model(&Transaction{}).Where("account_id = ?", req.AccountId),
		req.Type, req.Direction, req.From, req.To, req.MinAmount, req.MaxAmount, req.Query)
	if err != nil {
		return nil, err
	}
	return s.page(q, req.PageSize, req.PageToken)
}

func (s *txnServer) ListMyTransactions(ctx context.Context, req *transactionv1.ListMyTransactionsRequest) (*transactionv1.ListTransactionsResponse, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	q := s.db.WithContext(ctx).Model(&Transaction{}).Where("user_id = ?", p.UserID)
	if req.Type != "" {
		q = q.Where("type = ?", req.Type)
	}
	return s.page(q, req.PageSize, req.PageToken)
}

func (s *txnServer) SearchTransactions(ctx context.Context, req *transactionv1.SearchTransactionsRequest) (*transactionv1.ListTransactionsResponse, error) {
	if _, err := authz.RequireRole(ctx, authz.RoleOpsOfficer, authz.RoleFraudAnalyst, authz.RoleAuditor); err != nil {
		return nil, err
	}
	q := s.db.WithContext(ctx).Model(&Transaction{})
	if req.UserId != "" {
		q = q.Where("user_id = ?", req.UserId)
	}
	if req.AccountNumber != "" {
		q = q.Where("account_number = ?", req.AccountNumber)
	}
	if req.Type != "" {
		q = q.Where("type = ?", req.Type)
	}
	if req.MinAmount > 0 {
		q = q.Where("amount >= ?", req.MinAmount)
	}
	return s.page(q, req.PageSize, req.PageToken)
}

