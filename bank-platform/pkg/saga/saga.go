// Package saga records the steps of an orchestrated saga so each money
// movement shows exactly which steps ran, failed or were compensated.
package saga

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"
)

// Step statuses.
const (
	OK          = "OK"
	Failed      = "FAILED"
	Compensated = "COMPENSATED"
	Waiting     = "WAITING"
)

// Step is one saga step.
type Step struct {
	Name   string    `json:"name"`
	Status string    `json:"status"`
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
}

// Steps is stored as a jsonb column.
type Steps []Step

// Add appends a step.
func (s *Steps) Add(name, status, detail string) {
	*s = append(*s, Step{Name: name, Status: status, Detail: detail, At: time.Now().UTC()})
}

// Has reports whether a step with name completed OK.
func (s Steps) Has(name string) bool {
	for _, st := range s {
		if st.Name == name && st.Status == OK {
			return true
		}
	}
	return false
}

// Value implements driver.Valuer.
func (s Steps) Value() (driver.Value, error) {
	if s == nil {
		return "[]", nil
	}
	b, err := json.Marshal(s)
	return string(b), err
}

// Scan implements sql.Scanner.
func (s *Steps) Scan(v any) error {
	switch b := v.(type) {
	case []byte:
		return json.Unmarshal(b, s)
	case string:
		return json.Unmarshal([]byte(b), s)
	case nil:
		*s = nil
		return nil
	}
	return errors.New("saga.Steps: unsupported type")
}

// GormDataType sets the column type.
func (Steps) GormDataType() string { return "jsonb" }
