package main

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	ledgerv1 "github.com/dso202/bank-platform/gen/go/bank/ledger/v1"
	"github.com/dso202/bank-platform/pkg/authz"
	"github.com/dso202/bank-platform/pkg/ledgerx"
	"github.com/dso202/bank-platform/pkg/util"
)

// Account kinds.
const (
	KindCustomer = "CUSTOMER"
	KindSystem   = "SYSTEM"
)

// LedgerAccount holds the materialised balance of one account. Balance always
// equals credits minus debits over its postings (checked by TrialBalance).
type LedgerAccount struct {
	ID        string `gorm:"primaryKey"`
	Kind      string `gorm:"not null"`
	Name      string
	Balance   int64 `gorm:"not null;default:0"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// JournalEntry is one balanced, immutable set of postings. Corrections are
// new reversing entries, never edits.
type JournalEntry struct {
	ID              string `gorm:"primaryKey;type:uuid"`
	IdempotencyKey  string `gorm:"uniqueIndex;not null"`
	Description     string
	ReferenceType   string `gorm:"index:idx_entry_ref"`
	ReferenceID     string `gorm:"index:idx_entry_ref"`
	ReversesEntryID *string `gorm:"uniqueIndex"`
	CreatedAt       time.Time `gorm:"index"`
	Postings        []Posting `gorm:"foreignKey:EntryID"`
}

// Posting is one debit or credit line of an entry.
type Posting struct {
	ID           uint64 `gorm:"primaryKey;autoIncrement"`
	EntryID      string `gorm:"type:uuid;index;not null"`
	AccountID    string `gorm:"index:idx_posting_account;not null"`
	Direction    string `gorm:"not null"`
	Amount       int64  `gorm:"not null"`
	BalanceAfter int64  `gorm:"not null"`
	CreatedAt    time.Time `gorm:"index:idx_posting_account"`
}

var (
	entriesPosted = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "bank_ledger_entries_total", Help: "Journal entries by outcome.",
	}, []string{"reference_type", "outcome"})
	valuePosted = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "bank_ledger_value_posted_chhertrum_total", Help: "Value moved through the ledger, in chhertrum.",
	}, []string{"reference_type"})
)

type ledgerServer struct {
	ledgerv1.UnimplementedLedgerServiceServer
	db *gorm.DB
}

func (s *ledgerServer) ensureAccount(ctx context.Context, id, kind, name string) (*LedgerAccount, error) {
	acc := LedgerAccount{ID: id, Kind: kind, Name: name}
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&acc).Error
	if err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).First(&acc, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &acc, nil
}

func (s *ledgerServer) CreateLedgerAccount(ctx context.Context, req *ledgerv1.CreateLedgerAccountRequest) (*ledgerv1.LedgerAccount, error) {
	if req.AccountId == "" {
		return nil, util.Invalid("account_id is required")
	}
	kind := req.Kind
	if kind == "" {
		kind = KindCustomer
	}
	if kind != KindCustomer && kind != KindSystem {
		return nil, util.Invalid("kind must be CUSTOMER or SYSTEM")
	}
	acc, err := s.ensureAccount(ctx, req.AccountId, kind, req.Name)
	if err != nil {
		return nil, util.Internal(err)
	}
	return &ledgerv1.LedgerAccount{AccountId: acc.ID, Kind: acc.Kind, Name: acc.Name, Balance: acc.Balance}, nil
}

func validatePostings(ps []*ledgerv1.Posting) error {
	if len(ps) < 2 {
		return util.Invalid("an entry needs at least two postings")
	}
	var debits, credits int64
	for _, p := range ps {
		if p.AccountId == "" {
			return util.Invalid("posting account_id is required")
		}
		if p.Amount <= 0 {
			return util.Invalid("posting amounts must be positive")
		}
		switch p.Direction {
		case ledgerx.Debit:
			debits += p.Amount
		case ledgerx.Credit:
			credits += p.Amount
		default:
			return util.Invalid("posting direction must be DEBIT or CREDIT")
		}
	}
	if debits != credits {
		return util.Invalid("entry is not balanced: debits %d != credits %d", debits, credits)
	}
	return nil
}

func (s *ledgerServer) findByKey(ctx context.Context, key string) (*JournalEntry, error) {
	var e JournalEntry
	err := s.db.WithContext(ctx).Preload("Postings", func(db *gorm.DB) *gorm.DB { return db.Order("id") }).
		First(&e, "idempotency_key = ?", key).Error
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// PostEntries writes a balanced entry atomically. Replaying the same
// idempotency key returns the original entry and posts nothing.
func (s *ledgerServer) PostEntries(ctx context.Context, req *ledgerv1.PostEntriesRequest) (*ledgerv1.JournalEntry, error) {
	if req.IdempotencyKey == "" {
		return nil, util.Invalid("idempotency_key is required")
	}
	if err := validatePostings(req.Postings); err != nil {
		return nil, err
	}
	if e, err := s.findByKey(ctx, req.IdempotencyKey); err == nil {
		entriesPosted.WithLabelValues(req.ReferenceType, "replayed").Inc()
		return toProto(e), nil
	}
	e, err := s.post(ctx, req.IdempotencyKey, req.Description, req.ReferenceType, req.ReferenceId, nil, req.Postings)
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		// A concurrent request with the same key won the race.
		if e, err := s.findByKey(ctx, req.IdempotencyKey); err == nil {
			return toProto(e), nil
		}
	}
	if err != nil {
		entriesPosted.WithLabelValues(req.ReferenceType, "rejected").Inc()
		return nil, err
	}
	entriesPosted.WithLabelValues(req.ReferenceType, "posted").Inc()
	var total int64
	for _, p := range req.Postings {
		if p.Direction == ledgerx.Debit {
			total += p.Amount
		}
	}
	valuePosted.WithLabelValues(req.ReferenceType).Add(float64(total))
	return toProto(e), nil
}

func (s *ledgerServer) post(ctx context.Context, key, desc, refType, refID string, reverses *string, ps []*ledgerv1.Posting) (*JournalEntry, error) {
	entry := &JournalEntry{
		ID:              uuid.NewString(),
		IdempotencyKey:  key,
		Description:     desc,
		ReferenceType:   refType,
		ReferenceID:     refID,
		ReversesEntryID: reverses,
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ids := make([]string, 0, len(ps))
		seen := map[string]bool{}
		for _, p := range ps {
			if !seen[p.AccountId] {
				seen[p.AccountId] = true
				ids = append(ids, p.AccountId)
			}
		}
		// Lock in a fixed order so concurrent entries cannot deadlock.
		sort.Strings(ids)
		var accounts []LedgerAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id IN ?", ids).Order("id").Find(&accounts).Error; err != nil {
			return err
		}
		if len(accounts) != len(ids) {
			return status.Error(codes.NotFound, "ledger account not found")
		}
		byID := map[string]*LedgerAccount{}
		for i := range accounts {
			byID[accounts[i].ID] = &accounts[i]
		}
		now := time.Now().UTC()
		entry.CreatedAt = now
		for _, p := range ps {
			acc := byID[p.AccountId]
			if p.Direction == ledgerx.Credit {
				acc.Balance += p.Amount
			} else {
				acc.Balance -= p.Amount
			}
			entry.Postings = append(entry.Postings, Posting{
				EntryID: entry.ID, AccountID: p.AccountId, Direction: p.Direction,
				Amount: p.Amount, BalanceAfter: acc.Balance, CreatedAt: now,
			})
		}
		for _, acc := range byID {
			if acc.Kind == KindCustomer && acc.Balance < 0 {
				return status.Error(codes.FailedPrecondition, "insufficient funds")
			}
		}
		if err := tx.Create(entry).Error; err != nil {
			return err
		}
		for _, acc := range byID {
			if err := tx.Model(&LedgerAccount{}).Where("id = ?", acc.ID).
				Updates(map[string]any{"balance": acc.Balance, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entry, nil
}

// ReverseEntry posts the mirror image of an entry. An entry can be reversed once.
func (s *ledgerServer) ReverseEntry(ctx context.Context, req *ledgerv1.ReverseEntryRequest) (*ledgerv1.JournalEntry, error) {
	if req.EntryId == "" || req.IdempotencyKey == "" {
		return nil, util.Invalid("entry_id and idempotency_key are required")
	}
	if e, err := s.findByKey(ctx, req.IdempotencyKey); err == nil {
		return toProto(e), nil
	}
	var orig JournalEntry
	if err := s.db.WithContext(ctx).Preload("Postings").First(&orig, "id = ?", req.EntryId).Error; err != nil {
		return nil, util.NotFound("journal entry")
	}
	var existing JournalEntry
	if err := s.db.WithContext(ctx).Preload("Postings").First(&existing, "reverses_entry_id = ?", orig.ID).Error; err == nil {
		return toProto(&existing), nil
	}
	ps := make([]*ledgerv1.Posting, 0, len(orig.Postings))
	for _, p := range orig.Postings {
		dir := ledgerx.Credit
		if p.Direction == ledgerx.Credit {
			dir = ledgerx.Debit
		}
		ps = append(ps, &ledgerv1.Posting{AccountId: p.AccountID, Direction: dir, Amount: p.Amount})
	}
	desc := "Reversal: " + orig.Description
	if req.Reason != "" {
		desc += " (" + req.Reason + ")"
	}
	e, err := s.post(ctx, req.IdempotencyKey, desc, "REVERSAL", orig.ID, &orig.ID, ps)
	if err != nil {
		return nil, err
	}
	entriesPosted.WithLabelValues("REVERSAL", "posted").Inc()
	return toProto(e), nil
}

func (s *ledgerServer) GetBalance(ctx context.Context, req *ledgerv1.GetBalanceRequest) (*ledgerv1.Balance, error) {
	var acc LedgerAccount
	if err := s.db.WithContext(ctx).First(&acc, "id = ?", req.AccountId).Error; err != nil {
		return nil, util.NotFound("ledger account")
	}
	return &ledgerv1.Balance{AccountId: acc.ID, Balance: acc.Balance, AsOf: util.TS(time.Now())}, nil
}

func (s *ledgerServer) GetBalances(ctx context.Context, req *ledgerv1.GetBalancesRequest) (*ledgerv1.GetBalancesResponse, error) {
	var accs []LedgerAccount
	if len(req.AccountIds) > 0 {
		if err := s.db.WithContext(ctx).Where("id IN ?", req.AccountIds).Find(&accs).Error; err != nil {
			return nil, util.Internal(err)
		}
	}
	out := &ledgerv1.GetBalancesResponse{}
	now := util.TS(time.Now())
	for _, a := range accs {
		out.Balances = append(out.Balances, &ledgerv1.Balance{AccountId: a.ID, Balance: a.Balance, AsOf: now})
	}
	return out, nil
}

// GetBalanceAt returns the balance after the last posting strictly before req.At.
func (s *ledgerServer) GetBalanceAt(ctx context.Context, req *ledgerv1.GetBalanceAtRequest) (*ledgerv1.Balance, error) {
	at := time.Now()
	if req.At != nil {
		at = req.At.AsTime()
	}
	var p Posting
	err := s.db.WithContext(ctx).Where("account_id = ? AND created_at < ?", req.AccountId, at).
		Order("id DESC").Limit(1).Find(&p).Error
	if err != nil {
		return nil, util.Internal(err)
	}
	return &ledgerv1.Balance{AccountId: req.AccountId, Balance: p.BalanceAfter, AsOf: util.TS(at)}, nil
}

func (s *ledgerServer) ListAccountPostings(ctx context.Context, req *ledgerv1.ListAccountPostingsRequest) (*ledgerv1.ListAccountPostingsResponse, error) {
	type row struct {
		Posting
		Description   string
		ReferenceType string
		ReferenceID   string
	}
	q := s.db.WithContext(ctx).Table("postings").
		Select("postings.*, journal_entries.description, journal_entries.reference_type, journal_entries.reference_id").
		Joins("JOIN journal_entries ON journal_entries.id = postings.entry_id").
		Where("postings.account_id = ?", req.AccountId)
	if req.From != nil {
		q = q.Where("postings.created_at >= ?", req.From.AsTime())
	}
	if req.To != nil {
		q = q.Where("postings.created_at < ?", req.To.AsTime())
	}
	var rows []row
	if err := q.Order("postings.id").Limit(10000).Scan(&rows).Error; err != nil {
		return nil, util.Internal(err)
	}
	out := &ledgerv1.ListAccountPostingsResponse{}
	for _, r := range rows {
		out.Postings = append(out.Postings, &ledgerv1.AccountPosting{
			EntryId: r.EntryID, Direction: r.Direction, Amount: r.Amount, BalanceAfter: r.BalanceAfter,
			Description: r.Description, ReferenceType: r.ReferenceType, ReferenceId: r.ReferenceID,
			CreatedAt: util.TS(r.CreatedAt),
		})
	}
	return out, nil
}

// TrialBalance proves ledger integrity: total debits equal total credits, every
// entry balances, and every materialised balance matches its postings.
func (s *ledgerServer) TrialBalance(ctx context.Context, _ *ledgerv1.TrialBalanceRequest) (*ledgerv1.TrialBalanceResponse, error) {
	if _, err := authz.RequireRole(ctx, authz.RoleAuditor, authz.RoleAdmin); err != nil {
		return nil, err
	}
	db := s.db.WithContext(ctx)
	out := &ledgerv1.TrialBalanceResponse{}
	var totals struct{ Debits, Credits int64 }
	if err := db.Raw(`SELECT
		COALESCE(SUM(CASE WHEN direction = 'DEBIT' THEN amount END), 0) AS debits,
		COALESCE(SUM(CASE WHEN direction = 'CREDIT' THEN amount END), 0) AS credits
		FROM postings`).Scan(&totals).Error; err != nil {
		return nil, util.Internal(err)
	}
	out.TotalDebits, out.TotalCredits = totals.Debits, totals.Credits
	db.Model(&JournalEntry{}).Count(&out.EntryCount)
	db.Raw(`SELECT COUNT(*) FROM (SELECT entry_id FROM postings GROUP BY entry_id
		HAVING SUM(CASE WHEN direction = 'DEBIT' THEN amount ELSE -amount END) <> 0) t`).Scan(&out.UnbalancedEntries)
	db.Raw(`SELECT COUNT(*) FROM ledger_accounts a LEFT JOIN (
		SELECT account_id, SUM(CASE WHEN direction = 'CREDIT' THEN amount ELSE -amount END) AS net
		FROM postings GROUP BY account_id) p ON p.account_id = a.id
		WHERE a.balance <> COALESCE(p.net, 0)`).Scan(&out.BalanceMismatches)
	db.Raw(`SELECT COALESCE(SUM(balance), 0) FROM ledger_accounts WHERE kind = ?`, KindCustomer).Scan(&out.CustomerFunds)
	var sys []LedgerAccount
	db.Where("kind = ?", KindSystem).Order("id").Find(&sys)
	for _, a := range sys {
		out.SystemAccounts = append(out.SystemAccounts, &ledgerv1.LedgerAccount{AccountId: a.ID, Kind: a.Kind, Name: a.Name, Balance: a.Balance})
	}
	out.Balanced = out.TotalDebits == out.TotalCredits && out.UnbalancedEntries == 0 && out.BalanceMismatches == 0
	return out, nil
}

func (s *ledgerServer) ListEntries(ctx context.Context, req *ledgerv1.ListEntriesRequest) (*ledgerv1.ListEntriesResponse, error) {
	if _, err := authz.RequireRole(ctx, authz.RoleAuditor, authz.RoleAdmin); err != nil {
		return nil, err
	}
	limit, offset := util.Page(req.PageSize, req.PageToken)
	q := s.db.WithContext(ctx).Model(&JournalEntry{}).Preload("Postings", func(db *gorm.DB) *gorm.DB { return db.Order("id") })
	if req.AccountId != "" {
		q = q.Where("id IN (SELECT entry_id FROM postings WHERE account_id = ?)", req.AccountId)
	}
	var entries []JournalEntry
	if err := q.Order("created_at DESC").Limit(limit).Offset(offset).Find(&entries).Error; err != nil {
		return nil, util.Internal(err)
	}
	out := &ledgerv1.ListEntriesResponse{NextPageToken: util.NextToken(len(entries), limit, offset)}
	for i := range entries {
		out.Entries = append(out.Entries, toProto(&entries[i]))
	}
	return out, nil
}

func toProto(e *JournalEntry) *ledgerv1.JournalEntry {
	out := &ledgerv1.JournalEntry{
		Id: e.ID, IdempotencyKey: e.IdempotencyKey, Description: e.Description,
		ReferenceType: e.ReferenceType, ReferenceId: e.ReferenceID, CreatedAt: util.TS(e.CreatedAt),
	}
	if e.ReversesEntryID != nil {
		out.ReversesEntryId = *e.ReversesEntryID
	}
	for _, p := range e.Postings {
		out.Postings = append(out.Postings, &ledgerv1.Posting{
			AccountId: p.AccountID, Direction: p.Direction, Amount: p.Amount, BalanceAfter: p.BalanceAfter,
		})
	}
	return out
}
