// Package tasks owns durable, device-local database transfer execution records.
package tasks

import (
	"encoding/json"
	"errors"
)

type Kind string

const (
	KindBackup  Kind = "backup"
	KindRestore Kind = "restore"
	KindImport  Kind = "import"
)

type State string

const (
	StateQueued      State = "queued"
	StateRunning     State = "running"
	StateCancelling  State = "cancelling"
	StateSucceeded   State = "succeeded"
	StateFailed      State = "failed"
	StateCancelled   State = "cancelled"
	StateInterrupted State = "interrupted"
)

func (s State) Terminal() bool {
	return s == StateSucceeded || s == StateFailed || s == StateCancelled || s == StateInterrupted
}

type Certainty string

const (
	EffectsNone      Certainty = "none"
	EffectsKnown     Certainty = "known"
	EffectsUncertain Certainty = "uncertain"
)

var (
	ErrIdempotencyConflict    = errors.New("task_idempotency_conflict")
	ErrPreparationUnavailable = errors.New("task_preparation_unavailable")
	ErrRevisionConflict       = errors.New("task_revision_conflict")
	ErrNotFound               = errors.New("task_not_found")
	ErrInvalidRequest         = errors.New("task_invalid_request")
	ErrStopped                = errors.New("task_manager_stopped")
)

// Request contains frozen, non-secret execution parameters, never row data or SQL.
// Payload is private to the backend and is omitted from snapshots returned to clients.
type Request struct {
	Kind           Kind            `json:"kind"`
	IdempotencyKey string          `json:"idempotencyKey"`
	Fingerprint    string          `json:"fingerprint"`
	PreparationID  string          `json:"preparationId,omitempty"`
	WorkspaceID    string          `json:"workspaceId,omitempty"`
	ConnectionID   string          `json:"connectionId,omitempty"`
	JobID          string          `json:"jobId,omitempty"`
	Resources      []string        `json:"resources,omitempty"`
	Payload        json.RawMessage `json:"-"`
}
type Result struct {
	Artifact  string    `json:"artifact,omitempty"`
	Removed   int       `json:"removed,omitempty"`
	Records   int64     `json:"records"`
	Objects   int64     `json:"objects"`
	Files     int64     `json:"files"`
	Bytes     int64     `json:"bytes"`
	Warnings  []string  `json:"warnings,omitempty"`
	Certainty Certainty `json:"certainty"`
}
type Task struct {
	ID           string  `json:"id"`
	Kind         Kind    `json:"kind"`
	WorkspaceID  string  `json:"workspaceId,omitempty"`
	ConnectionID string  `json:"connectionId,omitempty"`
	JobID        string  `json:"jobId,omitempty"`
	State        State   `json:"state"`
	Revision     int64   `json:"revision"`
	CreatedAt    int64   `json:"createdAt"`
	UpdatedAt    int64   `json:"updatedAt"`
	StartedAt    int64   `json:"startedAt,omitempty"`
	FinishedAt   int64   `json:"finishedAt,omitempty"`
	ErrorCode    string  `json:"errorCode,omitempty"`
	Result       Result  `json:"result"`
	Request      Request `json:"-"`
}
type Preparation struct {
	ID          string
	Kind        Kind
	Fingerprint string
	ExpiresAt   int64
	Payload     json.RawMessage
}
type Filter struct {
	Kind        Kind
	JobID       string
	WorkspaceID string
	ActiveOnly  bool
	Limit       int
	// Cursor is the last returned task ID; ordering is stable by created_at and ID.
	Cursor string
}
type Page struct {
	Tasks      []Task `json:"tasks"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// Diagnostic is an explicitly allowlisted, payload-free domain failure.
// Raw driver/server messages must never be converted directly to this type.
type Diagnostic string

func (d Diagnostic) Error() string { return string(d) }
