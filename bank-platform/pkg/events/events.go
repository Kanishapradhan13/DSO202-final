// Package events defines the side effects that services emit through their
// outbox (audit records, notifications, transaction-history entries) and the
// handlers that deliver them.
package events

import (
	"context"
	"encoding/json"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	auditv1 "github.com/dso202/bank-platform/gen/go/bank/audit/v1"
	notificationv1 "github.com/dso202/bank-platform/gen/go/bank/notification/v1"
	transactionv1 "github.com/dso202/bank-platform/gen/go/bank/transaction/v1"
	"github.com/dso202/bank-platform/pkg/authz"
	"github.com/dso202/bank-platform/pkg/clients"
	"github.com/dso202/bank-platform/pkg/outbox"
)

// Outbox topics.
const (
	TopicAudit  = "audit.record"
	TopicNotify = "notification.send"
	TopicTxn    = "transaction.record"
)

func toJSON(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// Audit queues an audit record for a state-changing action.
func Audit(ctx context.Context, tx *gorm.DB, service, action, resourceType, resourceID, outcome string, before, after any) error {
	p := authz.FromContext(ctx)
	actor := p.UserID
	if actor == "" {
		actor = "anonymous"
	}
	req := &auditv1.RecordRequest{
		ActorId:      actor,
		ActorRoles:   joinRoles(p.Roles),
		Service:      service,
		Action:       action,
		ResourceType: resourceType,
		ResourceId:   resourceID,
		Outcome:      outcome,
		BeforeJson:   toJSON(before),
		AfterJson:    toJSON(after),
		RequestId:    p.RequestID,
		OccurredAt:   timestamppb.New(time.Now()),
	}
	b, err := protojson.Marshal(req)
	if err != nil {
		return err
	}
	_, err = outbox.Enqueue(tx, TopicAudit, b)
	return err
}

func joinRoles(r []string) string {
	out := ""
	for i, s := range r {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

// Notify queues a notification to a user using a named template.
func Notify(tx *gorm.DB, userID, template string, data map[string]string) error {
	return NotifyEmail(tx, userID, "", template, data)
}

// NotifyEmail queues a notification to an explicit email address (e.g. OTP before login).
func NotifyEmail(tx *gorm.DB, userID, email, template string, data map[string]string) error {
	b, err := protojson.Marshal(&notificationv1.SendRequest{UserId: userID, Email: email, Template: template, Data: data})
	if err != nil {
		return err
	}
	_, err = outbox.Enqueue(tx, TopicNotify, b)
	return err
}

// RecordTxn queues a transaction-history entry.
func RecordTxn(tx *gorm.DB, req *transactionv1.RecordTransactionRequest) error {
	if req.OccurredAt == nil {
		req.OccurredAt = timestamppb.Now()
	}
	b, err := protojson.Marshal(req)
	if err != nil {
		return err
	}
	_, err = outbox.Enqueue(tx, TopicTxn, b)
	return err
}

// Handlers returns outbox handlers that deliver events over gRPC.
func Handlers(c *clients.Clients) map[string]outbox.Handler {
	return map[string]outbox.Handler{
		TopicAudit: func(ctx context.Context, id string, payload []byte) error {
			var req auditv1.RecordRequest
			if err := protojson.Unmarshal(payload, &req); err != nil {
				return err
			}
			req.EventId = id
			_, err := c.Audit().Record(authz.SystemContext(ctx), &req)
			return err
		},
		TopicNotify: func(ctx context.Context, id string, payload []byte) error {
			var req notificationv1.SendRequest
			if err := protojson.Unmarshal(payload, &req); err != nil {
				return err
			}
			req.EventId = id
			_, err := c.Notification().Send(authz.SystemContext(ctx), &req)
			return err
		},
		TopicTxn: func(ctx context.Context, id string, payload []byte) error {
			var req transactionv1.RecordTransactionRequest
			if err := protojson.Unmarshal(payload, &req); err != nil {
				return err
			}
			_, err := c.Transaction().RecordTransaction(authz.SystemContext(ctx), &req)
			return err
		},
	}
}

// Dispatcher builds an outbox dispatcher for a service database.
func Dispatcher(db *gorm.DB, c *clients.Clients) *outbox.Dispatcher {
	return &outbox.Dispatcher{DB: db, Handlers: Handlers(c)}
}
