package port

import (
	"context"
	"errors"
	"time"
)

var (
	ErrManagementAccountDeleteNotFound              = errors.New("management account delete target not found")
	ErrManagementAccountDeleteAuthorizationInstance = errors.New("management account delete target is authorization instance")
)

type ManagementAccountDeleteInput struct {
	AccountID                string
	EffectiveSystemAccountID string
	CanAccessAll             bool
	DeletedBy                string
	DeletedAt                time.Time
}

type ManagementAccountDeleteSummary struct {
	ID              string
	SystemAccountID string
	Name            string
}

type ManagementAccountDeleteResult struct {
	Before            ManagementAccountDeleteSummary
	DeletedAccountIDs []string
	PageDataOwnerIDs  []string
}

type ManagementAccountDeleter interface {
	DeleteManagementAccount(ctx context.Context, input ManagementAccountDeleteInput) (ManagementAccountDeleteResult, error)
}
