// audit-service is an append-only, hash-chained, tamper-evident log of every
// state-changing action across the platform.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	auditv1 "github.com/dso202/bank-platform/gen/go/bank/audit/v1"
	"github.com/dso202/bank-platform/pkg/authz"
	"github.com/dso202/bank-platform/pkg/db"
	"github.com/dso202/bank-platform/pkg/server"
	"github.com/dso202/bank-platform/pkg/util"
)

const genesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

var recorded = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "bank_audit_events_total", Help: "Audit events recorded.",
}, []string{"service", "outcome"})

// Event is one audit record. Rows are never updated or deleted (enforced by a trigger).
type Event struct {
	ID           string `gorm:"primaryKey;type:uuid"`
	Sequence     int64  `gorm:"uniqueIndex;not null"`
	EventID      string `gorm:"uniqueIndex;not null"`
	ActorID      string `gorm:"index"`
	ActorRoles   string
	Service      string `gorm:"index"`
	Action       string `gorm:"index"`
	ResourceType string `gorm:"index:idx_audit_resource"`
	ResourceID   string `gorm:"index:idx_audit_resource"`
	Outcome      string
	BeforeJSON   string
	AfterJSON    string
	RequestID    string `gorm:"index"`
	PrevHash     string `gorm:"not null"`
	Hash         string `gorm:"not null"`
	OccurredAt   time.Time `gorm:"index"`
	RecordedAt   time.Time
}

func (Event) TableName() string { return "audit_events" }

const appendOnlySQL = `
CREATE OR REPLACE FUNCTION audit_events_immutable() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'audit_events is append-only';
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS audit_events_no_update ON audit_events;
CREATE TRIGGER audit_events_no_update BEFORE UPDATE OR DELETE ON audit_events
  FOR EACH ROW EXECUTE FUNCTION audit_events_immutable();
`

type auditServer struct {
	auditv1.UnimplementedAuditServiceServer
	db *gorm.DB
}

func main() {
	cfg := server.Init("audit-service")
	gdb := db.MustOpen(cfg, &Event{})
	if err := gdb.Exec(appendOnlySQL).Error; err != nil {
		panic(err)
	}
	if cfg.MigrateOnly {
		return
	}
	svc := &auditServer{db: gdb}
	server.Run(server.Service{
		Config:   cfg,
		Register: func(s *grpc.Server) { auditv1.RegisterAuditServiceServer(s, svc) },
	})
}

func chainHash(prev string, e *Event) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%d|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s",
		prev, e.Sequence, e.EventID, e.ActorID, e.ActorRoles, e.Service, e.Action, e.ResourceType, e.ResourceID,
		e.Outcome, e.BeforeJSON, e.AfterJSON, e.RequestID, e.OccurredAt.UTC().Format(time.RFC3339Nano))
	return hex.EncodeToString(h.Sum(nil))
}

func toProto(e *Event) *auditv1.AuditEvent {
	return &auditv1.AuditEvent{
		Id: e.ID, Sequence: e.Sequence, ActorId: e.ActorID, ActorRoles: e.ActorRoles, Service: e.Service, Action: e.Action,
		ResourceType: e.ResourceType, ResourceId: e.ResourceID, Outcome: e.Outcome, BeforeJson: e.BeforeJSON, AfterJson: e.AfterJSON,
		RequestId: e.RequestID, Hash: e.Hash, PrevHash: e.PrevHash, OccurredAt: util.TS(e.OccurredAt),
	}
}

// Record appends an event to the chain. Idempotent on event_id (outbox retries).
func (s *auditServer) Record(ctx context.Context, req *auditv1.RecordRequest) (*auditv1.AuditEvent, error) {
	if req.Action == "" || req.Service == "" {
		return nil, util.Invalid("service and action are required")
	}
	if req.EventId == "" {
		req.EventId = uuid.NewString()
	}
	var existing Event
	if err := s.db.WithContext(ctx).First(&existing, "event_id = ?", req.EventId).Error; err == nil {
		return toProto(&existing), nil
	}
	e := Event{
		ID: uuid.NewString(), EventID: req.EventId, ActorID: req.ActorId, ActorRoles: req.ActorRoles, Service: req.Service,
		Action: req.Action, ResourceType: req.ResourceType, ResourceID: req.ResourceId, Outcome: req.Outcome,
		BeforeJSON: req.BeforeJson, AfterJSON: req.AfterJson, RequestID: req.RequestId, RecordedAt: time.Now().UTC(),
	}
	if req.OccurredAt != nil {
		e.OccurredAt = req.OccurredAt.AsTime().UTC().Truncate(time.Microsecond)
	} else {
		e.OccurredAt = e.RecordedAt.Truncate(time.Microsecond)
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialise appends so the chain has no forks.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(7262)").Error; err != nil {
			return err
		}
		var last Event
		prev, seq := genesisHash, int64(1)
		if err := tx.Order("sequence DESC").Limit(1).Find(&last).Error; err != nil {
			return err
		}
		if last.ID != "" {
			prev, seq = last.Hash, last.Sequence+1
		}
		e.Sequence, e.PrevHash = seq, prev
		e.Hash = chainHash(prev, &e)
		return tx.Create(&e).Error
	})
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		if err := s.db.WithContext(ctx).First(&existing, "event_id = ?", req.EventId).Error; err == nil {
			return toProto(&existing), nil
		}
	}
	if err != nil {
		return nil, util.Internal(err)
	}
	recorded.WithLabelValues(e.Service, e.Outcome).Inc()
	return toProto(&e), nil
}

func (s *auditServer) ListEvents(ctx context.Context, req *auditv1.ListEventsRequest) (*auditv1.ListEventsResponse, error) {
	if _, err := authz.RequireRole(ctx, authz.RoleAuditor, authz.RoleFraudAnalyst); err != nil {
		return nil, err
	}
	limit, offset := util.Page(req.PageSize, req.PageToken)
	q := s.db.WithContext(ctx).Model(&Event{})
	for col, v := range map[string]string{
		"actor_id": req.ActorId, "service": req.Service, "resource_type": req.ResourceType,
		"resource_id": req.ResourceId, "request_id": req.RequestId,
	} {
		if v = strings.TrimSpace(v); v != "" {
			q = q.Where(col+" = ?", v)
		}
	}
	if v := strings.TrimSpace(req.Action); v != "" {
		q = q.Where("action LIKE ?", v+"%")
	}
	out := &auditv1.ListEventsResponse{}
	q.Count(&out.Total)
	var es []Event
	if err := q.Order("sequence DESC").Limit(limit).Offset(offset).Find(&es).Error; err != nil {
		return nil, util.Internal(err)
	}
	out.NextPageToken = util.NextToken(len(es), limit, offset)
	for i := range es {
		out.Events = append(out.Events, toProto(&es[i]))
	}
	return out, nil
}

// VerifyChain recomputes every hash; any edit, deletion or reordering breaks it.
func (s *auditServer) VerifyChain(ctx context.Context, _ *auditv1.VerifyChainRequest) (*auditv1.VerifyChainResponse, error) {
	if _, err := authz.RequireRole(ctx, authz.RoleAuditor); err != nil {
		return nil, err
	}
	out := &auditv1.VerifyChainResponse{Valid: true}
	prev := genesisHash
	var expectSeq int64 = 1
	var batch []Event
	err := s.db.WithContext(ctx).Order("sequence").FindInBatches(&batch, 1000, func(tx *gorm.DB, _ int) error {
		for i := range batch {
			e := &batch[i]
			if e.Sequence != expectSeq || e.PrevHash != prev || chainHash(prev, e) != e.Hash {
				out.Valid, out.FirstBrokenSequence = false, e.Sequence
				return errors.New("chain broken")
			}
			prev, expectSeq = e.Hash, expectSeq+1
			out.Checked++
		}
		return nil
	}).Error
	if err != nil && out.Valid {
		return nil, util.Internal(err)
	}
	out.HeadHash = prev
	return out, nil
}
