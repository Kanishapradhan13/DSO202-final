// fraud-service screens every money movement against configurable rules and
// queues suspicious ones for analyst review.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"google.golang.org/grpc"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	fraudv1 "github.com/dso202/bank-platform/gen/go/bank/fraud/v1"
	paymentv1 "github.com/dso202/bank-platform/gen/go/bank/payment/v1"
	transferv1 "github.com/dso202/bank-platform/gen/go/bank/transfer/v1"
	"github.com/dso202/bank-platform/pkg/authz"
	"github.com/dso202/bank-platform/pkg/clients"
	"github.com/dso202/bank-platform/pkg/db"
	"github.com/dso202/bank-platform/pkg/events"
	"github.com/dso202/bank-platform/pkg/server"
	"github.com/dso202/bank-platform/pkg/util"
)

const svcName = "fraud-service"

// Decisions.
const (
	Allow  = "ALLOW"
	Review = "REVIEW"
	Block  = "BLOCK"
)

var decisions = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "bank_fraud_decisions_total", Help: "Fraud screening decisions.",
}, []string{"decision", "channel"})

// Rule is a configurable screening rule.
type Rule struct {
	ID            string `gorm:"primaryKey"`
	Code          string `gorm:"uniqueIndex"`
	Description   string
	Action        string
	Threshold     int64
	WindowMinutes int32
	Weight        int32
	Enabled       bool
	UpdatedAt     time.Time
}

// Screening is one scored request; scoring is idempotent per reference.
type Screening struct {
	ID            string `gorm:"primaryKey;type:uuid"`
	UserID        string `gorm:"index"`
	AccountID     string `gorm:"index:idx_screen_account_time"`
	Amount        int64
	Channel       string
	Counterparty  string
	ReferenceType string `gorm:"uniqueIndex:idx_screen_ref"`
	ReferenceID   string `gorm:"uniqueIndex:idx_screen_ref"`
	Decision      string `gorm:"index"`
	Score         int32
	Reasons       string
	AlertID       string
	CreatedAt     time.Time `gorm:"index:idx_screen_account_time"`
}

// Alert is a screening that needs (or needed) attention.
type Alert struct {
	ID            string `gorm:"primaryKey;type:uuid"`
	UserID        string
	AccountID     string
	Amount        int64
	Channel       string
	Counterparty  string
	ReferenceType string
	ReferenceID   string
	Decision      string
	Score         int32
	Reasons       string
	Status        string `gorm:"index"`
	AnalystID     string
	AnalystNote   string
	CreatedAt     time.Time `gorm:"index"`
	DecidedAt     *time.Time
}

var seedRules = []Rule{
	{ID: "r1", Code: "AMOUNT_BLOCK", Description: "Single movement at or above the hard limit", Action: Block, Threshold: 50_000_000, Weight: 100, Enabled: true},
	{ID: "r2", Code: "AMOUNT_REVIEW", Description: "Large single movement", Action: Review, Threshold: 10_000_000, Weight: 60, Enabled: true},
	{ID: "r3", Code: "VELOCITY", Description: "Too many movements from one account within the window (threshold = count)", Action: Review, Threshold: 5, WindowMinutes: 10, Weight: 40, Enabled: true},
	{ID: "r4", Code: "NEW_COUNTERPARTY", Description: "First payment to a new beneficiary above the threshold", Action: Review, Threshold: 5_000_000, Weight: 30, Enabled: true},
	{ID: "r5", Code: "DAILY_VOLUME", Description: "Total screened today from one account above the threshold", Action: Review, Threshold: 30_000_000, Weight: 30, Enabled: true},
	{ID: "r6", Code: "NIGHT_TIME", Description: "Movement above the threshold between 00:00 and 05:00 Bhutan time", Action: Review, Threshold: 2_500_000, Weight: 20, Enabled: true},
}

type fraudServer struct {
	fraudv1.UnimplementedFraudServiceServer
	db *gorm.DB
	cl *clients.Clients
	tz *time.Location
}

func main() {
	cfg := server.Init(svcName)
	gdb := db.MustOpen(cfg, &Rule{}, &Screening{}, &Alert{})
	if cfg.MigrateOnly {
		return
	}
	if err := gdb.Clauses(clause.OnConflict{DoNothing: true}).Create(&seedRules).Error; err != nil {
		slog.Error("seed rules", "err", err)
	}
	cl := clients.New(svcName)
	defer cl.Close()
	tz, err := time.LoadLocation("Asia/Thimphu")
	if err != nil {
		tz = time.FixedZone("BTT", 6*3600)
	}
	svc := &fraudServer{db: gdb, cl: cl, tz: tz}
	server.Run(server.Service{
		Config:   cfg,
		Register: func(s *grpc.Server) { fraudv1.RegisterFraudServiceServer(s, svc) },
		Workers:  []server.Worker{events.Dispatcher(gdb, cl).Run},
	})
}

func severity(d string) int { return map[string]int{Allow: 0, Review: 1, Block: 2}[d] }

// Score evaluates the enabled rules. Repeated calls for the same reference
// return the first result, so saga retries see a consistent decision.
func (s *fraudServer) Score(ctx context.Context, req *fraudv1.ScoreRequest) (*fraudv1.ScoreResponse, error) {
	if req.ReferenceId == "" || req.Amount <= 0 {
		return nil, util.Invalid("reference_id and amount are required")
	}
	var prev Screening
	if err := s.db.WithContext(ctx).First(&prev, "reference_type = ? AND reference_id = ?", req.ReferenceType, req.ReferenceId).Error; err == nil {
		return screeningProto(&prev), nil
	}
	var rules []Rule
	s.db.WithContext(ctx).Where("enabled = ?", true).Find(&rules)
	now := time.Now().UTC()
	decision, score := Allow, int32(0)
	var reasons []string
	hit := func(r Rule, why string) {
		reasons = append(reasons, r.Code+": "+why)
		score += r.Weight
		if severity(r.Action) > severity(decision) {
			decision = r.Action
		}
	}
	for _, r := range rules {
		switch r.Code {
		case "AMOUNT_BLOCK", "AMOUNT_REVIEW":
			if req.Amount >= r.Threshold {
				hit(r, "amount "+util.Nu(req.Amount)+" ≥ "+util.Nu(r.Threshold))
			}
		case "VELOCITY":
			var n int64
			s.db.WithContext(ctx).Model(&Screening{}).
				Where("account_id = ? AND created_at >= ?", req.AccountId, now.Add(-time.Duration(r.WindowMinutes)*time.Minute)).Count(&n)
			if n+1 > r.Threshold {
				hit(r, fmt.Sprintf("%d movements in %d min", n+1, r.WindowMinutes))
			}
		case "NEW_COUNTERPARTY":
			if strings.HasPrefix(req.Counterparty, "NEW:") && req.Amount >= r.Threshold {
				hit(r, "first transfer to this beneficiary")
			}
		case "DAILY_VOLUME":
			var total int64
			s.db.WithContext(ctx).Model(&Screening{}).Select("COALESCE(SUM(amount), 0)").
				Where("account_id = ? AND created_at >= ? AND decision <> ?", req.AccountId, util.StartOfDay(now), Block).Scan(&total)
			if total+req.Amount >= r.Threshold {
				hit(r, "today's volume "+util.Nu(total+req.Amount))
			}
		case "NIGHT_TIME":
			if h := now.In(s.tz).Hour(); h < 5 && req.Amount >= r.Threshold {
				hit(r, "unusual hour")
			}
		}
	}
	if score > 100 {
		score = 100
	}
	sc := Screening{
		ID: uuid.NewString(), UserID: req.UserId, AccountID: req.AccountId, Amount: req.Amount, Channel: req.Channel,
		Counterparty: req.Counterparty, ReferenceType: req.ReferenceType, ReferenceID: req.ReferenceId,
		Decision: decision, Score: score, Reasons: strings.Join(reasons, "\n"),
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if decision != Allow {
			// Only transfers and payments wait for a human; card and branch
			// decisions are final in real time, so their alerts are informational.
			st := "CLOSED"
			if decision == Review && (req.ReferenceType == "TRANSFER" || req.ReferenceType == "PAYMENT") {
				st = "OPEN"
			}
			a := Alert{
				ID: uuid.NewString(), UserID: req.UserId, AccountID: req.AccountId, Amount: req.Amount, Channel: req.Channel,
				Counterparty: req.Counterparty, ReferenceType: req.ReferenceType, ReferenceID: req.ReferenceId,
				Decision: decision, Score: score, Reasons: sc.Reasons, Status: st,
			}
			if err := tx.Create(&a).Error; err != nil {
				return err
			}
			sc.AlertID = a.ID
			if err := events.Audit(ctx, tx, svcName, "fraud.flag", "alert", a.ID, "SUCCESS", nil,
				map[string]any{"decision": decision, "score": score, "reference": req.ReferenceType + ":" + req.ReferenceId}); err != nil {
				return err
			}
		}
		return tx.Create(&sc).Error
	})
	if err != nil {
		if err := s.db.WithContext(ctx).First(&prev, "reference_type = ? AND reference_id = ?", req.ReferenceType, req.ReferenceId).Error; err == nil {
			return screeningProto(&prev), nil
		}
		return nil, util.Internal(err)
	}
	decisions.WithLabelValues(decision, req.Channel).Inc()
	return screeningProto(&sc), nil
}

func split(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func screeningProto(s *Screening) *fraudv1.ScoreResponse {
	return &fraudv1.ScoreResponse{Decision: s.Decision, Score: s.Score, Reasons: split(s.Reasons), AlertId: s.AlertID}
}

func alertProto(a *Alert) *fraudv1.Alert {
	return &fraudv1.Alert{
		Id: a.ID, UserId: a.UserID, AccountId: a.AccountID, Amount: a.Amount, Channel: a.Channel, Counterparty: a.Counterparty,
		ReferenceType: a.ReferenceType, ReferenceId: a.ReferenceID, Decision: a.Decision, Score: a.Score, Reasons: split(a.Reasons),
		Status: a.Status, AnalystId: a.AnalystID, AnalystNote: a.AnalystNote, CreatedAt: util.TS(a.CreatedAt), DecidedAt: util.TSP(a.DecidedAt),
	}
}

func (s *fraudServer) ListAlerts(ctx context.Context, req *fraudv1.ListAlertsRequest) (*fraudv1.ListAlertsResponse, error) {
	if _, err := authz.RequireRole(ctx, authz.RoleFraudAnalyst, authz.RoleAuditor); err != nil {
		return nil, err
	}
	limit, offset := util.Page(req.PageSize, req.PageToken)
	q := s.db.WithContext(ctx).Model(&Alert{})
	if req.Status != "" {
		q = q.Where("status = ?", req.Status)
	}
	var as []Alert
	q.Order("created_at DESC").Limit(limit).Offset(offset).Find(&as)
	out := &fraudv1.ListAlertsResponse{NextPageToken: util.NextToken(len(as), limit, offset)}
	for i := range as {
		out.Alerts = append(out.Alerts, alertProto(&as[i]))
	}
	return out, nil
}

// DecideAlert records the analyst's decision and resumes or cancels the held movement.
func (s *fraudServer) DecideAlert(ctx context.Context, req *fraudv1.DecideAlertRequest) (*fraudv1.Alert, error) {
	p, err := authz.RequireRole(ctx, authz.RoleFraudAnalyst)
	if err != nil {
		return nil, err
	}
	var a Alert
	if err := s.db.WithContext(ctx).First(&a, "id = ?", req.AlertId).Error; err != nil {
		return nil, util.NotFound("alert")
	}
	if a.Status != "OPEN" {
		return nil, util.Precondition("alert is already %s", strings.ToLower(a.Status))
	}
	if a.UserID == p.UserID {
		return nil, util.Denied("you cannot decide an alert on your own transaction")
	}
	switch a.ReferenceType {
	case "TRANSFER":
		_, err = s.cl.Transfer().ResolveReview(ctx, &transferv1.ResolveReviewRequest{TransferId: a.ReferenceID, Approve: req.Approve, Note: req.Note})
	case "PAYMENT":
		_, err = s.cl.Payment().ResolveReview(ctx, &paymentv1.ResolveReviewRequest{PaymentId: a.ReferenceID, Approve: req.Approve, Note: req.Note})
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	a.Status, a.AnalystID, a.AnalystNote, a.DecidedAt = "REJECTED", p.UserID, strings.TrimSpace(req.Note), &now
	if req.Approve {
		a.Status = "APPROVED"
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&a).Error; err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "fraud.decide", "alert", a.ID, "SUCCESS",
			map[string]any{"status": "OPEN"}, map[string]any{"status": a.Status, "note": a.AnalystNote})
	})
	if err != nil {
		return nil, util.Internal(err)
	}
	return alertProto(&a), nil
}

func ruleProto(r *Rule) *fraudv1.Rule {
	return &fraudv1.Rule{Id: r.ID, Code: r.Code, Description: r.Description, Action: r.Action, Threshold: r.Threshold,
		WindowMinutes: r.WindowMinutes, Weight: r.Weight, Enabled: r.Enabled, UpdatedAt: util.TS(r.UpdatedAt)}
}

func (s *fraudServer) ListRules(ctx context.Context, _ *fraudv1.ListRulesRequest) (*fraudv1.ListRulesResponse, error) {
	if _, err := authz.RequireRole(ctx, authz.RoleFraudAnalyst, authz.RoleAuditor); err != nil {
		return nil, err
	}
	var rs []Rule
	s.db.WithContext(ctx).Order("id").Find(&rs)
	out := &fraudv1.ListRulesResponse{}
	for i := range rs {
		out.Rules = append(out.Rules, ruleProto(&rs[i]))
	}
	return out, nil
}

func (s *fraudServer) UpdateRule(ctx context.Context, req *fraudv1.UpdateRuleRequest) (*fraudv1.Rule, error) {
	if _, err := authz.RequireRole(ctx, authz.RoleFraudAnalyst); err != nil {
		return nil, err
	}
	var r Rule
	if err := s.db.WithContext(ctx).First(&r, "id = ?", req.RuleId).Error; err != nil {
		return nil, util.NotFound("rule")
	}
	if req.Threshold <= 0 {
		return nil, util.Invalid("threshold must be positive")
	}
	if req.Action != "" && !slices.Contains([]string{Review, Block}, req.Action) {
		return nil, util.Invalid("action must be REVIEW or BLOCK")
	}
	before := ruleProto(&r)
	r.Threshold, r.Enabled = req.Threshold, req.Enabled
	if req.Action != "" {
		r.Action = req.Action
	}
	if req.WindowMinutes > 0 {
		r.WindowMinutes = req.WindowMinutes
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&r).Error; err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "fraud.update_rule", "rule", r.ID, "SUCCESS",
			map[string]any{"threshold": before.Threshold, "enabled": before.Enabled, "action": before.Action},
			map[string]any{"threshold": r.Threshold, "enabled": r.Enabled, "action": r.Action})
	})
	if err != nil {
		return nil, util.Internal(err)
	}
	return ruleProto(&r), nil
}

func (s *fraudServer) GetStats(ctx context.Context, _ *fraudv1.GetStatsRequest) (*fraudv1.FraudStats, error) {
	if _, err := authz.RequireRole(ctx, authz.RoleFraudAnalyst, authz.RoleAuditor); err != nil {
		return nil, err
	}
	out := &fraudv1.FraudStats{}
	db := s.db.WithContext(ctx)
	db.Model(&Screening{}).Count(&out.Screened)
	db.Model(&Screening{}).Where("decision = ?", Allow).Count(&out.Allowed)
	db.Model(&Screening{}).Where("decision = ?", Review).Count(&out.Reviewed)
	db.Model(&Screening{}).Where("decision = ?", Block).Count(&out.Blocked)
	db.Model(&Alert{}).Where("status = ?", "OPEN").Count(&out.OpenAlerts)
	return out, nil
}
