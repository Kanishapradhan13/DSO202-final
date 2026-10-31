// Package outbox implements the transactional outbox pattern: side effects
// (audit records, notifications, history entries) are written to the service's
// own database in the same transaction as the business change, then delivered
// by a background dispatcher with retries. Data and events never diverge.
package outbox

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Event is one pending side effect.
type Event struct {
	ID            string    `gorm:"primaryKey;type:uuid"`
	Topic         string    `gorm:"index;not null"`
	Payload       []byte    `gorm:"type:jsonb;not null"`
	Attempts      int       `gorm:"not null;default:0"`
	NextAttemptAt time.Time `gorm:"index;not null"`
	SentAt        *time.Time `gorm:"index"`
	DeadAt        *time.Time
	LastError     string
	CreatedAt     time.Time
}

// TableName keeps the table name stable across services.
func (Event) TableName() string { return "outbox_events" }

// Enqueue stores payload (any JSON-marshalable value, or raw []byte JSON) for delivery.
// Call it with the same *gorm.DB transaction as the business change.
func Enqueue(tx *gorm.DB, topic string, payload any) (string, error) {
	var raw []byte
	switch p := payload.(type) {
	case []byte:
		raw = p
	default:
		b, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		raw = b
	}
	ev := Event{ID: uuid.NewString(), Topic: topic, Payload: raw, NextAttemptAt: time.Now().UTC()}
	return ev.ID, tx.Create(&ev).Error
}

// Handler delivers one event. eventID is stable across retries, for idempotency.
type Handler func(ctx context.Context, eventID string, payload []byte) error

// Dispatcher delivers pending events to their topic handlers.
type Dispatcher struct {
	DB          *gorm.DB
	Handlers    map[string]Handler
	Interval    time.Duration
	MaxAttempts int
}

// Run polls for due events until ctx is cancelled. Several replicas can run it:
// rows are claimed with FOR UPDATE SKIP LOCKED.
func (d *Dispatcher) Run(ctx context.Context) {
	if d.Interval == 0 {
		d.Interval = 500 * time.Millisecond
	}
	if d.MaxAttempts == 0 {
		d.MaxAttempts = 25
	}
	t := time.NewTicker(d.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for d.dispatchBatch(ctx) > 0 {
			}
		}
	}
}

func (d *Dispatcher) dispatchBatch(ctx context.Context) int {
	processed := 0
	err := d.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var events []Event
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("sent_at IS NULL AND dead_at IS NULL AND next_attempt_at <= ?", time.Now().UTC()).
			Order("created_at").Limit(25).Find(&events).Error; err != nil {
			return err
		}
		for i := range events {
			ev := &events[i]
			processed++
			h, ok := d.Handlers[ev.Topic]
			now := time.Now().UTC()
			if !ok {
				ev.DeadAt, ev.LastError = &now, "no handler for topic"
			} else {
				hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				err := h(hctx, ev.ID, ev.Payload)
				cancel()
				ev.Attempts++
				if err == nil {
					ev.SentAt, ev.LastError = &now, ""
				} else {
					ev.LastError = err.Error()
					backoff := time.Duration(math.Min(math.Pow(2, float64(ev.Attempts)), 300)) * time.Second
					ev.NextAttemptAt = now.Add(backoff)
					if ev.Attempts >= d.MaxAttempts {
						ev.DeadAt = &now
					}
					slog.Warn("outbox delivery failed", "topic", ev.Topic, "event_id", ev.ID, "attempt", ev.Attempts, "err", err)
				}
			}
			if err := tx.Save(ev).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		slog.Error("outbox batch failed", "err", err)
		return 0
	}
	return processed
}

// Purge deletes delivered events older than age. Run it periodically.
func Purge(db *gorm.DB, age time.Duration) {
	db.Where("sent_at IS NOT NULL AND sent_at < ?", time.Now().UTC().Add(-age)).Delete(&Event{})
}
