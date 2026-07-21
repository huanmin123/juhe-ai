// Code generated for the first-round management account list adapter.
package postgresqueries

import (
	"github.com/jackc/pgx/v5/pgtype"
)

type ListManagementAccountsParams struct {
	SystemAccountID string
	Keyword         string
	ProviderCode    string
	AccountType     string
	Statuses        []string
	TagIDs          []string
	Schedulable     string
	GroupID         string
	SortField       string
	SortOrder       string
	RowLimit        int32
	RowOffset       int32
}

type ListManagementAccountsRow struct {
	ID                     string
	SystemAccountID        string
	SystemAccountName      string
	Name                   string
	ProviderCode           string
	Type                   string
	Status                 string
	Schedulable            bool
	ConcurrencyLimit       int32
	Priority               int32
	SuperPriorityEnabled   bool
	FallbackEnabled        bool
	HealthCheckModel       string
	HealthCheckEndpointMode string
	AccountExpiresAt       pgtype.Timestamptz
	LastUsedAt             pgtype.Timestamptz
	AccessType             string
	AccountAuthorizationID pgtype.Text
	AuthorizationStatus    pgtype.Text
	AuthorizationExpiresAt pgtype.Timestamptz
	RequestCount           int64
	InputTokens            int64
	OutputTokens           int64
	TotalCost              float64
	QualityScore           pgtype.Int8
}
