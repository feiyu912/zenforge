package approval

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Inbox is a durable approval broker whose pending work can be inspected and
// resolved independently of the process waiting for the decision.
type Inbox interface {
	Broker
	Lookup(context.Context, string) (Request, error)
	List(context.Context, string) ([]Request, error)
	Submit(context.Context, Decision) error
}

// RequestRegistrar allows callers to make an approval request durable before
// publishing an event or checkpoint that refers to it.
type RequestRegistrar interface {
	RegisterRequest(context.Context, Request) error
}

type RecordStatus string

const (
	StatusPending  RecordStatus = "pending"
	StatusResolved RecordStatus = "resolved"
)

type Record struct {
	Request   Request      `json:"request"`
	Decision  *Decision    `json:"decision,omitempty"`
	Status    RecordStatus `json:"status"`
	CreatedAt time.Time    `json:"createdAt"`
	UpdatedAt time.Time    `json:"updatedAt"`
}

func (r Record) Validate() error {
	if err := r.Request.Validate(); err != nil {
		return fmt.Errorf("approval record request: %w", err)
	}
	if r.CreatedAt.IsZero() {
		return fmt.Errorf("approval record createdAt is required")
	}
	if r.UpdatedAt.IsZero() {
		return fmt.Errorf("approval record updatedAt is required")
	}
	if r.UpdatedAt.Before(r.CreatedAt) {
		return fmt.Errorf("approval record updatedAt cannot be before createdAt")
	}
	switch r.Status {
	case StatusPending:
		if r.Decision != nil {
			return fmt.Errorf("pending approval record cannot have a decision")
		}
	case StatusResolved:
		if r.Decision == nil {
			return fmt.Errorf("resolved approval record requires a decision")
		}
		if err := ValidateDecisionForRequest(r.Request, *r.Decision); err != nil {
			return fmt.Errorf("approval record decision: %w", err)
		}
	default:
		return fmt.Errorf("unsupported approval record status %q", r.Status)
	}
	return nil
}

type PendingStore interface {
	Register(context.Context, Request) error
	Get(context.Context, string) (Record, error)
	ListPending(context.Context, string) ([]Request, error)
	Resolve(context.Context, Decision) error
}

var (
	ErrRequestConflict  = errors.New("approval request conflict")
	ErrDecisionConflict = errors.New("approval decision conflict")
	ErrRequestExpired   = errors.New("approval request expired")
)
