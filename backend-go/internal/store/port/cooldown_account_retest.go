package port

import (
	"context"
	"time"
)

const CooldownAccountRetestMaxPageSize = 100

type CooldownAccountRetestCursor struct {
	CooldownUntil time.Time
	Priority      int
	CreatedAt     time.Time
	ID            string
}

type CooldownAccountRetestCandidate struct {
	ID                      string
	Name                    string
	ConfigRevision          int
	CooldownUntil           time.Time
	Priority                int
	CreatedAt               time.Time
	ObservationStartedAt    *time.Time
	SystemAccountID         string
	GroupID                 string
	HealthCheckModel        string
	HealthCheckEndpointMode string
	MaxPauseMinutes         int
	MaxRecoveryHours        int
}

type CooldownAccountRetestPage struct {
	Candidates []CooldownAccountRetestCandidate
	NextCursor *CooldownAccountRetestCursor
}

type CooldownAccountRetestListInput struct {
	Now    time.Time
	Limit  int
	Cursor *CooldownAccountRetestCursor
}

type CooldownAccountRetestStore interface {
	ListDueCooldownAccountRetests(context.Context, CooldownAccountRetestListInput) (CooldownAccountRetestPage, error)
	FindDueCooldownAccountRetest(context.Context, string, time.Time) (CooldownAccountRetestCandidate, bool, error)
}

type CooldownAccountRetestTask struct {
	AccountID            string
	ConfigRevision       int
	ObservationStartedAt *time.Time
	MaxPauseMinutes      int
	MaxRecoveryHours     int
}

type CooldownAccountRetestProbeResult struct {
	Outcome    string
	StatusCode int
	ErrorCode  string
	Message    string
	TraceID    string
}

type CooldownAccountRetestOutcomeStore interface {
	RecordCooldownAccountRetestSuccess(context.Context, CooldownAccountRetestTask) error
	DeferCooldownAccountRetest(context.Context, CooldownAccountRetestTask, time.Duration) error
	RecordCooldownAccountRetestFailure(context.Context, CooldownAccountRetestTask, CooldownAccountRetestProbeResult) error
}
