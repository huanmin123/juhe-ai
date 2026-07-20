package port

import (
	"context"
	"time"
)

type ManagementAccountForceActivateInput struct {
	AccountID      string
	OwnerSystemID  string
	ConfigRevision int
	Now            time.Time
	Schedule       map[string]any
}

type ManagementAccountForceActivateResult struct {
	AccountID     string
	OwnerSystemID string
	Status        string
	Schedulable   bool
	BeforeStatus  string
	AfterStatus   string
}

type ManagementAccountForceActivator interface {
	ForceActivatePendingAccount(context.Context, ManagementAccountForceActivateInput) (ManagementAccountForceActivateResult, bool, error)
}
