package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	accountv1 "github.com/dso202/bank-platform/gen/go/bank/account/v1"
	customerv1 "github.com/dso202/bank-platform/gen/go/bank/customer/v1"
	kycv1 "github.com/dso202/bank-platform/gen/go/bank/kyc/v1"
	ledgerv1 "github.com/dso202/bank-platform/gen/go/bank/ledger/v1"
	"github.com/dso202/bank-platform/pkg/authz"
	"github.com/dso202/bank-platform/pkg/clients"
	"github.com/dso202/bank-platform/pkg/events"
	"github.com/dso202/bank-platform/pkg/util"
)

const svcName = "account-service"

// Account statuses.
const (
	Active = "ACTIVE"
	Frozen = "FROZEN"
	Closed = "CLOSED"
)

// Hold statuses.
const (
	HoldActive   = "ACTIVE"
	HoldReleased = "RELEASED"
)

const maxAccountsPerUser = 5

// Account is a customer deposit account. Its balance lives in ledger-service.
type Account struct {
	ID                 string `gorm:"primaryKey;type:uuid"`
	AccountNumber      string `gorm:"uniqueIndex;not null"`
	UserID             string `gorm:"type:uuid;index;not null"`
	HolderName         string `gorm:"not null"`
	Type               string `gorm:"not null"`
	Nickname           string
	Status             string `gorm:"index;not null"`
	StatusReason       string
	Currency           string `gorm:"not null;default:BTN"`
	DailyTransferLimit int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
	ClosedAt           *time.Time
}

// Hold reserves funds while a money movement is in flight.
type Hold struct {
	ID         string `gorm:"primaryKey;type:uuid"`
	AccountID  string `gorm:"type:uuid;index;not null"`
	Amount     int64  `gorm:"not null"`
	Reference  string `gorm:"uniqueIndex;not null"`
	Status     string `gorm:"index;not null"`
	CreatedAt  time.Time
	ReleasedAt *time.Time
}

type accountServer struct {
	accountv1.UnimplementedAccountServiceServer
	db           *gorm.DB
	cl           *clients.Clients
	defaultLimit int64
	holdTTL      time.Duration
}

func (s *accountServer) toProto(a *Account, balance, held int64) *accountv1.Account {
	avail := balance - held
	if avail < 0 {
		avail = 0
	}
	return &accountv1.Account{
		Id: a.ID, AccountNumber: a.AccountNumber, UserId: a.UserID, HolderName: a.HolderName, Type: a.Type,
		Nickname: a.Nickname, Status: a.Status, Currency: a.Currency, Balance: balance, AvailableBalance: avail,
		HeldAmount: held, DailyTransferLimit: a.DailyTransferLimit, StatusReason: a.StatusReason, CreatedAt: util.TS(a.CreatedAt),
	}
}

func (s *accountServer) heldAmounts(ctx context.Context, db *gorm.DB, ids []string) map[string]int64 {
	type row struct {
		AccountID string
		Total     int64
	}
	var rows []row
	db.WithContext(ctx).Model(&Hold{}).Select("account_id, SUM(amount) AS total").
		Where("account_id IN ? AND status = ?", ids, HoldActive).Group("account_id").Scan(&rows)
	out := map[string]int64{}
	for _, r := range rows {
		out[r.AccountID] = r.Total
	}
	return out
}

// enrich adds ledger balances and held amounts to accounts.
func (s *accountServer) enrich(ctx context.Context, accs []Account) ([]*accountv1.Account, error) {
	if len(accs) == 0 {
		return nil, nil
	}
	ids := make([]string, len(accs))
	for i, a := range accs {
		ids[i] = a.ID
	}
	bal, err := s.cl.Ledger().GetBalances(ctx, &ledgerv1.GetBalancesRequest{AccountIds: ids})
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "balances unavailable: %v", status.Convert(err).Message())
	}
	balances := map[string]int64{}
	for _, b := range bal.Balances {
		balances[b.AccountId] = b.Balance
	}
	held := s.heldAmounts(ctx, s.db, ids)
	out := make([]*accountv1.Account, len(accs))
	for i := range accs {
		out[i] = s.toProto(&accs[i], balances[accs[i].ID], held[accs[i].ID])
	}
	return out, nil
}

func (s *accountServer) newAccountNumber(ctx context.Context, accType string) string {
	prefix := "1010"
	if accType == "CURRENT" {
		prefix = "2020"
	}
	for {
		n := prefix + util.RandomDigits(8)
		var c int64
		s.db.WithContext(ctx).Model(&Account{}).Where("account_number = ?", n).Count(&c)
		if c == 0 {
			return n
		}
	}
}

func (s *accountServer) OpenAccount(ctx context.Context, req *accountv1.OpenAccountRequest) (*accountv1.Account, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	accType := strings.ToUpper(req.Type)
	if accType != "SAVINGS" && accType != "CURRENT" {
		return nil, util.Invalid("type must be SAVINGS or CURRENT")
	}
	kyc, err := s.cl.Kyc().GetKycStatus(ctx, &kycv1.GetKycStatusRequest{UserId: p.UserID})
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "KYC check unavailable: %v", status.Convert(err).Message())
	}
	if kyc.Status != "APPROVED" {
		return nil, util.Precondition("KYC must be approved before opening an account (current status: %s)", kyc.Status)
	}
	cust, err := s.cl.Customer().GetCustomerByUser(ctx, &customerv1.GetCustomerByUserRequest{UserId: p.UserID})
	if err != nil {
		return nil, util.Precondition("customer profile not found")
	}
	var count int64
	s.db.WithContext(ctx).Model(&Account{}).Where("user_id = ? AND status <> ?", p.UserID, Closed).Count(&count)
	if count >= maxAccountsPerUser {
		return nil, util.Precondition("you can hold at most %d open accounts", maxAccountsPerUser)
	}
	a := Account{
		ID: uuid.NewString(), AccountNumber: s.newAccountNumber(ctx, accType), UserID: p.UserID, HolderName: cust.FullName,
		Type: accType, Nickname: strings.TrimSpace(req.Nickname), Status: Active, Currency: "BTN", DailyTransferLimit: s.defaultLimit,
	}
	if a.Nickname == "" {
		a.Nickname = map[string]string{"SAVINGS": "Savings", "CURRENT": "Current"}[accType]
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&a).Error; err != nil {
			return err
		}
		if _, err := s.cl.Ledger().CreateLedgerAccount(ctx, &ledgerv1.CreateLedgerAccountRequest{
			AccountId: a.ID, Kind: "CUSTOMER", Name: a.AccountNumber,
		}); err != nil {
			return status.Errorf(codes.Unavailable, "ledger unavailable: %v", status.Convert(err).Message())
		}
		if err := events.Notify(tx, p.UserID, "ACCOUNT_OPENED", map[string]string{"account_number": a.AccountNumber, "type": accType}); err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "account.open", "account", a.ID, "SUCCESS", nil,
			map[string]any{"account_number": a.AccountNumber, "type": accType})
	})
	if err != nil {
		return nil, err
	}
	return s.toProto(&a, 0, 0), nil
}

func (s *accountServer) ListMyAccounts(ctx context.Context, _ *accountv1.ListMyAccountsRequest) (*accountv1.ListAccountsResponse, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	var accs []Account
	if err := s.db.WithContext(ctx).Where("user_id = ? AND status <> ?", p.UserID, Closed).Order("created_at").Find(&accs).Error; err != nil {
		return nil, util.Internal(err)
	}
	out, err := s.enrich(ctx, accs)
	if err != nil {
		return nil, err
	}
	return &accountv1.ListAccountsResponse{Accounts: out}, nil
}

func (s *accountServer) load(ctx context.Context, id string) (*Account, error) {
	var a Account
	if _, err := uuid.Parse(id); err != nil {
		return nil, util.NotFound("account")
	}
	if err := s.db.WithContext(ctx).First(&a, "id = ?", id).Error; err != nil {
		return nil, util.NotFound("account")
	}
	return &a, nil
}

func (s *accountServer) GetAccount(ctx context.Context, req *accountv1.GetAccountRequest) (*accountv1.Account, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.load(ctx, req.AccountId)
	if err != nil {
		return nil, err
	}
	if !p.CanAccessUser(a.UserID) {
		return nil, util.NotFound("account")
	}
	out, err := s.enrich(ctx, []Account{*a})
	if err != nil {
		return nil, err
	}
	return out[0], nil
}

func maskName(n string) string {
	parts := strings.Fields(n)
	for i, w := range parts {
		r := []rune(w)
		if len(r) > 2 {
			parts[i] = string(r[0]) + strings.Repeat("*", len(r)-2) + string(r[len(r)-1])
		}
	}
	return strings.Join(parts, " ")
}

func (s *accountServer) LookupAccount(ctx context.Context, req *accountv1.LookupAccountRequest) (*accountv1.AccountLookup, error) {
	if _, err := authz.RequireUser(ctx); err != nil {
		return nil, err
	}
	var a Account
	if err := s.db.WithContext(ctx).First(&a, "account_number = ? AND status <> ?", strings.TrimSpace(req.AccountNumber), Closed).Error; err != nil {
		return nil, util.NotFound("account")
	}
	return &accountv1.AccountLookup{AccountId: a.ID, AccountNumber: a.AccountNumber, HolderName: maskName(a.HolderName), Type: a.Type, Status: a.Status}, nil
}

func (s *accountServer) CloseAccount(ctx context.Context, req *accountv1.CloseAccountRequest) (*accountv1.Account, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.load(ctx, req.AccountId)
	if err != nil || (a.UserID != p.UserID && !p.HasRole(authz.RoleOpsOfficer)) {
		return nil, util.NotFound("account")
	}
	if a.Status == Closed {
		return nil, util.Precondition("account is already closed")
	}
	bal, err := s.cl.Ledger().GetBalance(ctx, &ledgerv1.GetBalanceRequest{AccountId: a.ID})
	if err != nil {
		return nil, status.Error(codes.Unavailable, "ledger unavailable")
	}
	if bal.Balance != 0 {
		return nil, util.Precondition("move the remaining balance (%s) out before closing", util.Nu(bal.Balance))
	}
	if held := s.heldAmounts(ctx, s.db, []string{a.ID})[a.ID]; held > 0 {
		return nil, util.Precondition("account has pending transactions")
	}
	now := time.Now().UTC()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(a).Updates(map[string]any{"status": Closed, "closed_at": now, "status_reason": "Closed by customer"}).Error; err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "account.close", "account", a.ID, "SUCCESS", map[string]any{"status": a.Status}, map[string]any{"status": Closed})
	})
	if err != nil {
		return nil, util.Internal(err)
	}
	a.Status = Closed
	return s.toProto(a, 0, 0), nil
}

func (s *accountServer) ListAccounts(ctx context.Context, req *accountv1.ListAccountsRequest) (*accountv1.ListAccountsResponse, error) {
	if _, err := authz.RequireRole(ctx, authz.StaffRoles...); err != nil {
		return nil, err
	}
	limit, offset := util.Page(req.PageSize, req.PageToken)
	q := s.db.WithContext(ctx).Model(&Account{})
	if req.UserId != "" {
		q = q.Where("user_id = ?", req.UserId)
	}
	if req.Status != "" {
		q = q.Where("status = ?", req.Status)
	}
	if v := strings.TrimSpace(req.Query); v != "" {
		q = q.Where("account_number LIKE ? OR LOWER(holder_name) LIKE ?", v+"%", "%"+strings.ToLower(v)+"%")
	}
	var accs []Account
	if err := q.Order("created_at DESC").Limit(limit).Offset(offset).Find(&accs).Error; err != nil {
		return nil, util.Internal(err)
	}
	out, err := s.enrich(ctx, accs)
	if err != nil {
		return nil, err
	}
	return &accountv1.ListAccountsResponse{Accounts: out, NextPageToken: util.NextToken(len(accs), limit, offset)}, nil
}

func (s *accountServer) SetAccountStatus(ctx context.Context, req *accountv1.SetAccountStatusRequest) (*accountv1.Account, error) {
	if _, err := authz.RequireRole(ctx, authz.RoleOpsOfficer); err != nil {
		return nil, err
	}
	if req.Status != Active && req.Status != Frozen {
		return nil, util.Invalid("status must be ACTIVE or FROZEN")
	}
	if req.Status == Frozen && strings.TrimSpace(req.Reason) == "" {
		return nil, util.Invalid("a reason is required to freeze an account")
	}
	a, err := s.load(ctx, req.AccountId)
	if err != nil {
		return nil, err
	}
	if a.Status == Closed {
		return nil, util.Precondition("account is closed")
	}
	before := a.Status
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(a).Updates(map[string]any{"status": req.Status, "status_reason": req.Reason}).Error; err != nil {
			return err
		}
		tmpl := "ACCOUNT_UNFROZEN"
		if req.Status == Frozen {
			tmpl = "ACCOUNT_FROZEN"
		}
		if err := events.Notify(tx, a.UserID, tmpl, map[string]string{"account_number": a.AccountNumber, "reason": req.Reason}); err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "account.set_status", "account", a.ID, "SUCCESS",
			map[string]any{"status": before}, map[string]any{"status": req.Status, "reason": req.Reason})
	})
	if err != nil {
		return nil, util.Internal(err)
	}
	a.Status, a.StatusReason = req.Status, req.Reason
	out, err := s.enrich(ctx, []Account{*a})
	if err != nil {
		return s.toProto(a, 0, 0), nil
	}
	return out[0], nil
}

// ValidateAccount checks that an account exists, optionally belongs to a user,
// and can send (for_debit) or receive money. Frozen accounts may still receive.
func (s *accountServer) ValidateAccount(ctx context.Context, req *accountv1.ValidateAccountRequest) (*accountv1.Account, error) {
	a, err := s.load(ctx, req.AccountId)
	if err != nil {
		return nil, err
	}
	if req.OwnerUserId != "" && a.UserID != req.OwnerUserId {
		return nil, util.NotFound("account")
	}
	switch {
	case a.Status == Closed:
		return nil, util.Precondition("account %s is closed", a.AccountNumber)
	case req.ForDebit && a.Status == Frozen:
		return nil, util.Precondition("account %s is frozen", a.AccountNumber)
	}
	return s.toProto(a, 0, 0), nil
}

func (s *accountServer) ResolveAccountNumber(ctx context.Context, req *accountv1.ResolveAccountNumberRequest) (*accountv1.Account, error) {
	var a Account
	if err := s.db.WithContext(ctx).First(&a, "account_number = ?", strings.TrimSpace(req.AccountNumber)).Error; err != nil {
		return nil, util.NotFound("account")
	}
	return s.toProto(&a, 0, 0), nil
}

func holdProto(h *Hold) *accountv1.Hold {
	return &accountv1.Hold{Id: h.ID, AccountId: h.AccountID, Amount: h.Amount, Reference: h.Reference, Status: h.Status, CreatedAt: util.TS(h.CreatedAt)}
}

// PlaceHold reserves funds. The account row is locked while the available
// balance (ledger balance minus active holds) is checked, so concurrent holds
// on one account cannot overspend it. Idempotent on reference.
func (s *accountServer) PlaceHold(ctx context.Context, req *accountv1.PlaceHoldRequest) (*accountv1.Hold, error) {
	if req.Amount <= 0 || req.Reference == "" {
		return nil, util.Invalid("amount and reference are required")
	}
	var h Hold
	if err := s.db.WithContext(ctx).First(&h, "reference = ?", req.Reference).Error; err == nil {
		return holdProto(&h), nil
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var a Account
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&a, "id = ?", req.AccountId).Error; err != nil {
			return util.NotFound("account")
		}
		if a.Status != Active {
			return util.Precondition("account %s is %s", a.AccountNumber, strings.ToLower(a.Status))
		}
		bal, err := s.cl.Ledger().GetBalance(ctx, &ledgerv1.GetBalanceRequest{AccountId: a.ID})
		if err != nil {
			return status.Error(codes.Unavailable, "ledger unavailable")
		}
		held := s.heldAmounts(ctx, tx, []string{a.ID})[a.ID]
		if bal.Balance-held < req.Amount {
			return status.Errorf(codes.FailedPrecondition, "insufficient funds: available %s", util.Nu(bal.Balance-held))
		}
		h = Hold{ID: uuid.NewString(), AccountID: a.ID, Amount: req.Amount, Reference: req.Reference, Status: HoldActive}
		return tx.Create(&h).Error
	})
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		if err := s.db.WithContext(ctx).First(&h, "reference = ?", req.Reference).Error; err == nil {
			return holdProto(&h), nil
		}
	}
	if err != nil {
		return nil, err
	}
	return holdProto(&h), nil
}

func (s *accountServer) ReleaseHold(ctx context.Context, req *accountv1.ReleaseHoldRequest) (*accountv1.Hold, error) {
	var h Hold
	q := s.db.WithContext(ctx)
	var err error
	switch {
	case req.HoldId != "":
		err = q.First(&h, "id = ?", req.HoldId).Error
	case req.Reference != "":
		err = q.First(&h, "reference = ?", req.Reference).Error
	default:
		return nil, util.Invalid("hold_id or reference is required")
	}
	if err != nil {
		// Releasing a hold that was never placed is a no-op (saga compensation).
		return &accountv1.Hold{Reference: req.Reference, Status: HoldReleased}, nil
	}
	if h.Status == HoldActive {
		now := time.Now().UTC()
		s.db.WithContext(ctx).Model(&Hold{}).Where("id = ? AND status = ?", h.ID, HoldActive).
			Updates(map[string]any{"status": HoldReleased, "released_at": now})
		h.Status, h.ReleasedAt = HoldReleased, &now
	}
	return holdProto(&h), nil
}

func (s *accountServer) ListAllAccountIds(ctx context.Context, req *accountv1.ListAllAccountIdsRequest) (*accountv1.ListAllAccountIdsResponse, error) {
	q := s.db.WithContext(ctx).Model(&Account{})
	if req.Status != "" {
		q = q.Where("status = ?", req.Status)
	} else {
		q = q.Where("status <> ?", Closed)
	}
	var ids []string
	if err := q.Pluck("id", &ids).Error; err != nil {
		return nil, util.Internal(err)
	}
	return &accountv1.ListAllAccountIdsResponse{AccountIds: ids}, nil
}

// expireHolds releases holds left behind by a crashed saga.
func (s *accountServer) expireHolds(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			res := s.db.Model(&Hold{}).Where("status = ? AND created_at < ?", HoldActive, time.Now().UTC().Add(-s.holdTTL)).
				Updates(map[string]any{"status": HoldReleased, "released_at": time.Now().UTC()})
			if res.RowsAffected > 0 {
				slog.Warn("expired stale holds", "count", res.RowsAffected)
			}
		}
	}
}
