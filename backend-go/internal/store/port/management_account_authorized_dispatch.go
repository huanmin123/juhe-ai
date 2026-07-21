package port

import (
	"context"
	"errors"
	"time"
)

var (
	ErrManagementAccountAuthorizedDispatchPendingTest = errors.New("authorized account is pending test")
	ErrManagementAccountAuthorizedDispatchExclusive   = errors.New("super priority and fallback are mutually exclusive")
	ErrManagementAccountAuthorizedDispatchUnavailable = errors.New("authorized account is unavailable")
)

type ManagementAccountAuthorizedDispatchInput struct {
	AccountID                string
	EffectiveSystemAccountID string
	CanAccessAll             bool
	Status                   *string
	Priority                 *int
	SuperPriorityEnabled     *bool
	FallbackEnabled          *bool
	ClearFailureState        bool
	UpdatedAt                time.Time
}

type ManagementAccountAuthorizedDispatchAccount struct {
	ID                     string
	SystemAccountID        string
	Name                   string
	ProviderCode           string
	Type                   string
	Status                 string
	Schedulable            bool
	ConcurrencyLimit       int
	Priority               int
	SuperPriorityEnabled   bool
	FallbackEnabled        bool
	BoundGroupID           string
	BoundGroupName         string
	AccountAuthorizationID string
}

type ManagementAccountAuthorizedDispatchResult struct {
	Account       ManagementAccountAuthorizedDispatchAccount
	ChangedFields []string
}

type ManagementAccountAuthorizedDispatcher interface {
	UpdateManagementAccountAuthorizedDispatch(context.Context, ManagementAccountAuthorizedDispatchInput) (ManagementAccountAuthorizedDispatchResult, bool, error)
}
