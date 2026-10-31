package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	accountv1 "github.com/dso202/bank-platform/gen/go/bank/account/v1"
	cardv1 "github.com/dso202/bank-platform/gen/go/bank/card/v1"
	fraudv1 "github.com/dso202/bank-platform/gen/go/bank/fraud/v1"
	ledgerv1 "github.com/dso202/bank-platform/gen/go/bank/ledger/v1"
	transactionv1 "github.com/dso202/bank-platform/gen/go/bank/transaction/v1"
	"github.com/dso202/bank-platform/pkg/authz"
	"github.com/dso202/bank-platform/pkg/clients"
	"github.com/dso202/bank-platform/pkg/events"
	"github.com/dso202/bank-platform/pkg/ledgerx"
	"github.com/dso202/bank-platform/pkg/util"
)

const svcName = "card-service"

// Card statuses.
const (
	Inactive = "INACTIVE"
	Active   = "ACTIVE"
	Blocked  = "BLOCKED"
	Lost     = "LOST"
)

const (
	bin             = "457123" // test BIN
	maxDailyLimit   = 20_000_000
	defaultDaily    = 5_000_000 // Nu. 50,000
	defaultPerTxn   = 2_500_000 // Nu. 25,000
	maxPinFailures  = 3
)

var authorizations = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "bank_card_authorizations_total", Help: "Card authorisations by outcome.",
}, []string{"status", "channel"})

// Card is a debit card. The PAN is never stored: only a token, a peppered
// hash and the last four digits (PCI DSS principle). CVV is never stored.
type Card struct {
	ID                  string `gorm:"primaryKey;type:uuid"`
	UserID              string `gorm:"type:uuid;index;not null"`
	AccountID           string `gorm:"type:uuid;index;not null"`
	AccountNumber       string
	PanToken            string `gorm:"uniqueIndex;not null"`
	PanHash             string `gorm:"uniqueIndex;not null"`
	Last4               string `gorm:"not null"`
	ExpiryMonth         int32
	ExpiryYear          int32
	CardholderName      string
	Status              string `gorm:"index;not null"`
	BlockReason         string
	PinHash             string
	PinFailures         int
	DailyLimit          int64
	PerTransactionLimit int64
	OnlineEnabled       bool
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// Authorization is one card purchase attempt.
type Authorization struct {
	ID             string `gorm:"primaryKey;type:uuid"`
	CardID         string `gorm:"type:uuid;not null;index;uniqueIndex:idx_auth_idem"`
	UserID         string `gorm:"type:uuid;index"`
	IdempotencyKey string `gorm:"not null;uniqueIndex:idx_auth_idem"`
	Merchant       string
	Amount         int64
	Channel        string
	Status         string `gorm:"index"`
	DeclineReason  string
	LedgerEntryID  string
	CreatedAt      time.Time `gorm:"index"`
}

type cardServer struct {
	cardv1.UnimplementedCardServiceServer
	db     *gorm.DB
	cl     *clients.Clients
	pepper string
}

func luhnDigit(s string) byte {
	sum := 0
	double := true
	for i := len(s) - 1; i >= 0; i-- {
		d := int(s[i] - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return byte('0' + (10-sum%10)%10)
}

func (s *cardServer) hashPAN(pan string) string {
	h := sha256.Sum256([]byte(s.pepper + pan))
	return hex.EncodeToString(h[:])
}

func (s *cardServer) spentToday(ctx context.Context, cardID string) int64 {
	var total int64
	s.db.WithContext(ctx).Model(&Authorization{}).Select("COALESCE(SUM(amount), 0)").
		Where("card_id = ? AND status = ? AND created_at >= ?", cardID, "APPROVED", util.StartOfDay(time.Now())).Scan(&total)
	return total
}

func (s *cardServer) toProto(ctx context.Context, c *Card) *cardv1.Card {
	return &cardv1.Card{
		Id: c.ID, UserId: c.UserID, AccountId: c.AccountID, AccountNumber: c.AccountNumber,
		MaskedPan: bin[:4] + " " + bin[4:] + "•• •••• " + c.Last4, Last4: c.Last4,
		ExpiryMonth: c.ExpiryMonth, ExpiryYear: c.ExpiryYear, CardholderName: c.CardholderName, Status: c.Status,
		BlockReason: c.BlockReason, DailyLimit: c.DailyLimit, PerTransactionLimit: c.PerTransactionLimit,
		OnlineEnabled: c.OnlineEnabled, SpentToday: s.spentToday(ctx, c.ID), Network: "VISA (test)", CreatedAt: util.TS(c.CreatedAt),
	}
}

func (s *cardServer) own(ctx context.Context, cardID string) (*Card, authz.Principal, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, p, err
	}
	var c Card
	if _, err := uuid.Parse(cardID); err != nil {
		return nil, p, util.NotFound("card")
	}
	if err := s.db.WithContext(ctx).First(&c, "id = ?", cardID).Error; err != nil || !p.CanAccessUser(c.UserID) {
		return nil, p, util.NotFound("card")
	}
	return &c, p, nil
}

func (s *cardServer) IssueCard(ctx context.Context, req *cardv1.IssueCardRequest) (*cardv1.Card, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	acc, err := s.cl.Account().ValidateAccount(ctx, &accountv1.ValidateAccountRequest{AccountId: req.AccountId, OwnerUserId: p.UserID, ForDebit: true})
	if err != nil {
		return nil, err
	}
	var n int64
	s.db.WithContext(ctx).Model(&Card{}).Where("account_id = ? AND status IN ?", acc.Id, []string{Inactive, Active, Blocked}).Count(&n)
	if n > 0 {
		return nil, util.Precondition("this account already has a debit card")
	}
	body := bin + util.RandomDigits(9)
	pan := body + string(luhnDigit(body))
	exp := time.Now().AddDate(4, 0, 0)
	c := Card{
		ID: uuid.NewString(), UserID: p.UserID, AccountID: acc.Id, AccountNumber: acc.AccountNumber,
		PanToken: "tok_" + strings.ReplaceAll(uuid.NewString(), "-", ""), PanHash: s.hashPAN(pan), Last4: pan[12:],
		ExpiryMonth: int32(exp.Month()), ExpiryYear: int32(exp.Year()), CardholderName: strings.ToUpper(acc.HolderName),
		Status: Inactive, DailyLimit: defaultDaily, PerTransactionLimit: defaultPerTxn, OnlineEnabled: true,
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&c).Error; err != nil {
			return err
		}
		if err := events.Notify(tx, p.UserID, "CARD_ISSUED", map[string]string{"last4": c.Last4, "account_number": acc.AccountNumber}); err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "card.issue", "card", c.ID, "SUCCESS", nil, map[string]any{"last4": c.Last4, "account_id": acc.Id})
	})
	if err != nil {
		return nil, util.Internal(err)
	}
	return s.toProto(ctx, &c), nil
}

func (s *cardServer) ListMyCards(ctx context.Context, _ *cardv1.ListMyCardsRequest) (*cardv1.ListCardsResponse, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	var cs []Card
	s.db.WithContext(ctx).Where("user_id = ?", p.UserID).Order("created_at DESC").Find(&cs)
	out := &cardv1.ListCardsResponse{}
	for i := range cs {
		out.Cards = append(out.Cards, s.toProto(ctx, &cs[i]))
	}
	return out, nil
}

func (s *cardServer) ListCards(ctx context.Context, req *cardv1.ListCardsRequest) (*cardv1.ListCardsResponse, error) {
	if _, err := authz.RequireRole(ctx, authz.RoleOpsOfficer, authz.RoleFraudAnalyst, authz.RoleAuditor); err != nil {
		return nil, err
	}
	limit, offset := util.Page(req.PageSize, req.PageToken)
	q := s.db.WithContext(ctx).Model(&Card{})
	if req.UserId != "" {
		q = q.Where("user_id = ?", req.UserId)
	}
	var cs []Card
	q.Order("created_at DESC").Limit(limit).Offset(offset).Find(&cs)
	out := &cardv1.ListCardsResponse{NextPageToken: util.NextToken(len(cs), limit, offset)}
	for i := range cs {
		out.Cards = append(out.Cards, s.toProto(ctx, &cs[i]))
	}
	return out, nil
}

func (s *cardServer) GetCard(ctx context.Context, req *cardv1.GetCardRequest) (*cardv1.Card, error) {
	c, _, err := s.own(ctx, req.CardId)
	if err != nil {
		return nil, err
	}
	return s.toProto(ctx, c), nil
}

var pinRe = regexp.MustCompile(`^\d{4}$`)

func (s *cardServer) ActivateCard(ctx context.Context, req *cardv1.ActivateCardRequest) (*cardv1.Card, error) {
	c, p, err := s.own(ctx, req.CardId)
	if err != nil {
		return nil, err
	}
	if c.UserID != p.UserID {
		return nil, util.Denied("only the cardholder can activate a card")
	}
	if c.Status != Inactive {
		return nil, util.Precondition("card is already %s", strings.ToLower(c.Status))
	}
	if !pinRe.MatchString(req.Pin) {
		return nil, util.Invalid("PIN must be 4 digits")
	}
	if req.Pin == "0000" || req.Pin == "1234" || req.Pin == "1111" {
		return nil, util.Invalid("choose a less predictable PIN")
	}
	h, _ := bcrypt.GenerateFromPassword([]byte(req.Pin), bcrypt.DefaultCost)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(c).Updates(map[string]any{"status": Active, "pin_hash": string(h), "pin_failures": 0}).Error; err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "card.activate", "card", c.ID, "SUCCESS", map[string]any{"status": Inactive}, map[string]any{"status": Active})
	})
	if err != nil {
		return nil, util.Internal(err)
	}
	c.Status = Active
	return s.toProto(ctx, c), nil
}

func (s *cardServer) BlockCard(ctx context.Context, req *cardv1.BlockCardRequest) (*cardv1.Card, error) {
	c, _, err := s.own(ctx, req.CardId)
	if err != nil {
		return nil, err
	}
	if c.Status == Lost {
		return nil, util.Precondition("card is permanently blocked")
	}
	reason := strings.ToUpper(req.Reason)
	newStatus := Blocked
	switch reason {
	case "LOST", "STOLEN":
		newStatus = Lost
	case "", "TEMPORARY":
		reason = "TEMPORARY"
	default:
		return nil, util.Invalid("reason must be TEMPORARY, LOST or STOLEN")
	}
	before := c.Status
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(c).Updates(map[string]any{"status": newStatus, "block_reason": reason}).Error; err != nil {
			return err
		}
		if err := events.Notify(tx, c.UserID, "CARD_BLOCKED", map[string]string{"last4": c.Last4, "reason": reason}); err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "card.block", "card", c.ID, "SUCCESS", map[string]any{"status": before}, map[string]any{"status": newStatus, "reason": reason})
	})
	if err != nil {
		return nil, util.Internal(err)
	}
	c.Status, c.BlockReason = newStatus, reason
	return s.toProto(ctx, c), nil
}

func (s *cardServer) UnblockCard(ctx context.Context, req *cardv1.UnblockCardRequest) (*cardv1.Card, error) {
	c, _, err := s.own(ctx, req.CardId)
	if err != nil {
		return nil, err
	}
	if c.Status != Blocked {
		return nil, util.Precondition("only temporarily blocked cards can be unblocked")
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(c).Updates(map[string]any{"status": Active, "block_reason": "", "pin_failures": 0}).Error; err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "card.unblock", "card", c.ID, "SUCCESS", map[string]any{"status": Blocked}, map[string]any{"status": Active})
	})
	if err != nil {
		return nil, util.Internal(err)
	}
	c.Status, c.BlockReason = Active, ""
	return s.toProto(ctx, c), nil
}

func (s *cardServer) SetLimits(ctx context.Context, req *cardv1.SetLimitsRequest) (*cardv1.Card, error) {
	c, _, err := s.own(ctx, req.CardId)
	if err != nil {
		return nil, err
	}
	if req.DailyLimit <= 0 || req.DailyLimit > maxDailyLimit {
		return nil, util.Invalid("daily limit must be between Nu. 0.01 and %s", util.Nu(maxDailyLimit))
	}
	if req.PerTransactionLimit <= 0 || req.PerTransactionLimit > req.DailyLimit {
		return nil, util.Invalid("per-transaction limit must be positive and not above the daily limit")
	}
	before := map[string]any{"daily": c.DailyLimit, "per_txn": c.PerTransactionLimit, "online": c.OnlineEnabled}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(c).Updates(map[string]any{
			"daily_limit": req.DailyLimit, "per_transaction_limit": req.PerTransactionLimit, "online_enabled": req.OnlineEnabled,
		}).Error; err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "card.set_limits", "card", c.ID, "SUCCESS", before,
			map[string]any{"daily": req.DailyLimit, "per_txn": req.PerTransactionLimit, "online": req.OnlineEnabled})
	})
	if err != nil {
		return nil, util.Internal(err)
	}
	c.DailyLimit, c.PerTransactionLimit, c.OnlineEnabled = req.DailyLimit, req.PerTransactionLimit, req.OnlineEnabled
	return s.toProto(ctx, c), nil
}

func authProto(a *Authorization) *cardv1.Authorization {
	return &cardv1.Authorization{
		Id: a.ID, CardId: a.CardID, Merchant: a.Merchant, Amount: a.Amount, Channel: a.Channel, Status: a.Status,
		DeclineReason: a.DeclineReason, LedgerEntryId: a.LedgerEntryID, CreatedAt: util.TS(a.CreatedAt),
	}
}

// Authorize simulates a card-network authorisation: card checks, PIN, limits,
// fraud score, then hold → ledger post → release, all in real time.
func (s *cardServer) Authorize(ctx context.Context, req *cardv1.AuthorizeRequest) (*cardv1.Authorization, error) {
	c, p, err := s.own(ctx, req.CardId)
	if err != nil {
		return nil, err
	}
	if c.UserID != p.UserID {
		return nil, util.Denied("only the cardholder can make purchases")
	}
	key := authz.IdempotencyKey(ctx)
	if key == "" {
		key = req.IdempotencyKey
	}
	if key == "" {
		key = uuid.NewString()
	}
	var existing Authorization
	if err := s.db.WithContext(ctx).First(&existing, "card_id = ? AND idempotency_key = ?", c.ID, key).Error; err == nil {
		return authProto(&existing), nil
	}
	channel := strings.ToUpper(req.Channel)
	if channel != "ONLINE" {
		channel = "POS"
	}
	merchant := strings.TrimSpace(req.Merchant)
	if merchant == "" {
		return nil, util.Invalid("merchant is required")
	}
	if req.Amount <= 0 {
		return nil, util.Invalid("amount must be positive")
	}
	a := &Authorization{ID: uuid.NewString(), CardID: c.ID, UserID: c.UserID, IdempotencyKey: key, Merchant: merchant, Amount: req.Amount, Channel: channel}
	decline := func(reason string) (*cardv1.Authorization, error) {
		a.Status, a.DeclineReason = "DECLINED", reason
		authorizations.WithLabelValues("DECLINED", channel).Inc()
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(a).Error; err != nil {
				return err
			}
			if err := events.Notify(tx, c.UserID, "CARD_DECLINED", map[string]string{"amount": util.Nu(a.Amount), "merchant": merchant, "reason": reason, "last4": c.Last4}); err != nil {
				return err
			}
			return events.Audit(ctx, tx, svcName, "card.authorize", "card", c.ID, "FAILURE", nil, map[string]any{"amount": a.Amount, "reason": reason})
		})
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			_ = s.db.WithContext(ctx).First(a, "card_id = ? AND idempotency_key = ?", c.ID, key).Error
		}
		return authProto(a), nil
	}
	now := time.Now()
	switch {
	case c.Status != Active:
		return decline("card is " + strings.ToLower(c.Status))
	case int(now.Year()) > int(c.ExpiryYear) || (int(now.Year()) == int(c.ExpiryYear) && int(now.Month()) > int(c.ExpiryMonth)):
		return decline("card expired")
	case channel == "ONLINE" && !c.OnlineEnabled:
		return decline("online payments are disabled for this card")
	}
	if channel == "POS" {
		if bcrypt.CompareHashAndPassword([]byte(c.PinHash), []byte(req.Pin)) != nil {
			fails := c.PinFailures + 1
			updates := map[string]any{"pin_failures": fails}
			if fails >= maxPinFailures {
				updates["status"], updates["block_reason"] = Blocked, "PIN_TRIES_EXCEEDED"
			}
			s.db.WithContext(ctx).Model(c).Updates(updates)
			return decline("incorrect PIN")
		}
		if c.PinFailures > 0 {
			s.db.WithContext(ctx).Model(c).Update("pin_failures", 0)
		}
	}
	if req.Amount > c.PerTransactionLimit {
		return decline("exceeds per-transaction limit of " + util.Nu(c.PerTransactionLimit))
	}
	if spent := s.spentToday(ctx, c.ID); spent+req.Amount > c.DailyLimit {
		return decline("exceeds daily card limit (" + util.Nu(c.DailyLimit-spent) + " remaining)")
	}
	score, err := s.cl.Fraud().Score(ctx, &fraudv1.ScoreRequest{
		UserId: c.UserID, AccountId: c.AccountID, Amount: req.Amount, Channel: "CARD",
		Counterparty: "MERCHANT:" + merchant, ReferenceType: "CARD_AUTH", ReferenceId: a.ID,
	})
	if err != nil {
		return decline("issuer unavailable (risk check)")
	}
	if score.Decision != "ALLOW" {
		return decline("declined by risk rules: " + strings.Join(score.Reasons, "; "))
	}
	ref := "card:" + a.ID
	if _, err := s.cl.Account().PlaceHold(ctx, &accountv1.PlaceHoldRequest{AccountId: c.AccountID, Amount: req.Amount, Reference: ref}); err != nil {
		if status.Code(err) == codes.FailedPrecondition {
			return decline("insufficient funds")
		}
		return decline("issuer unavailable")
	}
	entry, err := s.cl.Ledger().PostEntries(ctx, &ledgerv1.PostEntriesRequest{
		IdempotencyKey: ref, Description: "Card purchase · " + merchant, ReferenceType: "CARD_PURCHASE", ReferenceId: a.ID,
		Postings: ledgerx.Move(c.AccountID, ledgerx.CardSettlement, req.Amount),
	})
	_, _ = s.cl.Account().ReleaseHold(ctx, &accountv1.ReleaseHoldRequest{Reference: ref})
	if err != nil {
		return decline("insufficient funds")
	}
	a.Status, a.LedgerEntryID = "APPROVED", entry.Id
	authorizations.WithLabelValues("APPROVED", channel).Inc()
	bal := ledgerx.BalanceAfter(entry, c.AccountID)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(a).Error; err != nil {
			return err
		}
		if err := events.RecordTxn(tx, &transactionv1.RecordTransactionRequest{
			AccountId: c.AccountID, AccountNumber: c.AccountNumber, UserId: c.UserID, Type: "CARD_PURCHASE", Direction: ledgerx.Debit,
			Amount: req.Amount, BalanceAfter: bal, Counterparty: merchant, Description: "Card ••" + c.Last4 + " · " + channel,
			ReferenceType: "CARD_PURCHASE", ReferenceId: a.ID, LedgerEntryId: entry.Id,
		}); err != nil {
			return err
		}
		if err := events.Notify(tx, c.UserID, "CARD_PURCHASE", map[string]string{"amount": util.Nu(req.Amount), "merchant": merchant, "last4": c.Last4, "balance": util.Nu(bal)}); err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "card.authorize", "card", c.ID, "SUCCESS", nil, map[string]any{"amount": req.Amount, "merchant": merchant, "ledger_entry_id": entry.Id})
	})
	if err != nil {
		return nil, util.Internal(err)
	}
	return authProto(a), nil
}

func (s *cardServer) ListAuthorizations(ctx context.Context, req *cardv1.ListAuthorizationsRequest) (*cardv1.ListAuthorizationsResponse, error) {
	c, _, err := s.own(ctx, req.CardId)
	if err != nil {
		return nil, err
	}
	var as []Authorization
	s.db.WithContext(ctx).Where("card_id = ?", c.ID).Order("created_at DESC").Limit(50).Find(&as)
	out := &cardv1.ListAuthorizationsResponse{}
	for i := range as {
		out.Authorizations = append(out.Authorizations, authProto(&as[i]))
	}
	return out, nil
}
