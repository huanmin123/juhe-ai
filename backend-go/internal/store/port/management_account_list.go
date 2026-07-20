package port

import (
	"context"
	"time"
)

type ManagementAccountListSort struct {
	Field string
	Order string
}

type ManagementAccountListInput struct {
	SystemAccountID string
	Keyword         string
	ProviderCode    string
	GroupID         string
	Type            string
	Statuses        []string
	TagIDs          []string
	Schedulable     string
	Sorts           []ManagementAccountListSort
	Limit           int
	Offset          int
}

type ManagementAccountListRow struct {
	ID                     string
	SystemAccountID        string
	SystemAccountName      string
	Name                   string
	ProviderCode           string
	Type                   string
	Status                 string
	Schedulable            bool
	ConcurrencyLimit       int
	Priority               int
	SuperPriorityEnabled   bool
	FallbackEnabled        bool
	AccountExpiresAt       *time.Time
	LastUsedAt             *time.Time
	AccessType             string
	AccountAuthorizationID string
	AuthorizationStatus    string
	AuthorizationExpiresAt *time.Time
	RequestCount           int64
	InputTokens            int64
	OutputTokens           int64
	TotalCost              float64
	QualityScore           *int64
}

type ManagementAccountListPage struct {
	Rows    []ManagementAccountListRow
	HasMore bool
}

type ManagementAccountListReader interface {
	ListManagementAccounts(ctx context.Context, input ManagementAccountListInput) (ManagementAccountListPage, error)
}
