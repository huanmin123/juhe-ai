package port

import (
	"context"
	"errors"
	"time"
)

var (
	ErrManagementAccountUpdateProviderInvalid = errors.New("management account update provider invalid")
	ErrManagementAccountUpdateGroupInvalid    = errors.New("management account update group invalid")
	ErrManagementAccountUpdateNameExists      = errors.New("management account update name exists")
)

type ManagementAccountUpdateTargetInput struct {
	AccountID                string
	EffectiveSystemAccountID string
	CanAccessAll             bool
}

type ManagementAccountUpdateTarget struct {
	ID                   string
	SystemAccountID      string
	OwnerSystemAccountID string
	AccessType           string
	ProviderCode         string
	ProviderProfileID    string
	Type                 string
	ConfigRevision       int
	CredentialsEncrypted string
	Status               string
}

type ManagementAccountUpdateInput struct {
	AccountID                string
	EffectiveSystemAccountID string
	CanAccessAll             bool
	ExpectedConfigRevision   int
	CredentialsEncrypted     string
	HasCredentials           bool
	Updates                  map[string]any
	UpdatedAt                time.Time
}

type ManagementAccountUpdateResult struct {
	AccountID            string
	SystemAccountID      string
	OwnerSystemAccountID string
	Before               map[string]any
	After                map[string]any
	ChangedFields        []string
}

type ManagementAccountUpdater interface {
	LoadManagementAccountUpdateTarget(context.Context, ManagementAccountUpdateTargetInput) (ManagementAccountUpdateTarget, bool, error)
	UpdateManagementAccount(context.Context, ManagementAccountUpdateInput) (ManagementAccountUpdateResult, bool, error)
}
