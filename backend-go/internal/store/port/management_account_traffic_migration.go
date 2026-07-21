package port

import (
	"context"
	"errors"
	"time"
)

var (
	ErrManagementAccountTrafficMigrationNotFound          = errors.New("account traffic migration target not found")
	ErrManagementAccountTrafficMigrationSameAccount       = errors.New("account traffic migration target equals source")
	ErrManagementAccountTrafficMigrationDifferentOwner    = errors.New("account traffic migration owner mismatch")
	ErrManagementAccountTrafficMigrationDifferentProvider = errors.New("account traffic migration provider mismatch")
	ErrManagementAccountTrafficMigrationDifferentGroup    = errors.New("account traffic migration group mismatch")
	ErrManagementAccountTrafficMigrationTargetUnavailable = errors.New("account traffic migration target unavailable")
	ErrManagementAccountTrafficMigrationStateChanged      = errors.New("account traffic migration source state changed")
)

type ManagementAccountTrafficMigrationSourceStatus string

const (
	ManagementAccountTrafficMigrationTemporaryUnavailable ManagementAccountTrafficMigrationSourceStatus = "temporary_unavailable"
	ManagementAccountTrafficMigrationDisabled             ManagementAccountTrafficMigrationSourceStatus = "disabled"
	ManagementAccountTrafficMigrationUnchanged            ManagementAccountTrafficMigrationSourceStatus = "unchanged"
)

type ManagementAccountTrafficMigrationInput struct {
	SourceAccountID          string
	TargetAccountID          string
	EffectiveSystemAccountID string
	CanAccessAll             bool
	SourceStatus             ManagementAccountTrafficMigrationSourceStatus
	UpdatedAt                time.Time
}

type ManagementAccountTrafficMigrationAccount struct {
	ID                     string
	SystemAccountID        string
	OwnerSystemAccountID   string
	Name                   string
	ProviderCode           string
	Type                   string
	Status                 string
	Schedulable            bool
	CooldownUntil          time.Time
	BoundGroupID           string
	AccountAuthorizationID string
	AccessType             string
}

type ManagementAccountTrafficMigrationResult struct {
	SourceAccount       ManagementAccountTrafficMigrationAccount
	TargetAccount       ManagementAccountTrafficMigrationAccount
	GroupID             string
	SourceCooldownUntil time.Time
	SourceChanged       bool
}

type ManagementAccountTrafficMigrator interface {
	MigrateManagementAccountTraffic(context.Context, ManagementAccountTrafficMigrationInput) (ManagementAccountTrafficMigrationResult, bool, error)
}
