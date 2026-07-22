package port

import (
	"context"
	"errors"
)

var ErrResponseInspectionPolicyConflict = errors.New("response inspection policy conflict")

type ResponseInspectionPolicyMatch struct {
	ClientProfiles       []string `json:"clientProfiles,omitempty"`
	OutputTextIncludes   []string `json:"outputTextIncludes,omitempty"`
	OutputTextExcludes   []string `json:"outputTextExcludes,omitempty"`
	ErrorCodes           []string `json:"errorCodes,omitempty"`
	ErrorTypes           []string `json:"errorTypes,omitempty"`
	ErrorMessageIncludes []string `json:"errorMessageIncludes,omitempty"`
	FinishReasons        []string `json:"finishReasons,omitempty"`
	JSONPathsExists      []string `json:"jsonPathsExists,omitempty"`
	RawTextIncludes      []string `json:"rawTextIncludes,omitempty"`
}

type ResponseInspectionPolicy struct {
	ID           string                        `json:"id"`
	DefaultRule  bool                          `json:"defaultRule"`
	Editable     bool                          `json:"editable"`
	Name         string                        `json:"name"`
	Enabled      bool                          `json:"enabled"`
	Priority     int                           `json:"priority"`
	ScopeType    string                        `json:"scopeType"`
	ProtocolCode string                        `json:"protocolCode"`
	ProviderCode *string                       `json:"providerCode,omitempty"`
	Match        ResponseInspectionPolicyMatch `json:"match"`
	Action       string                        `json:"action"`
	Notes        *string                       `json:"notes,omitempty"`
	CreatedAt    string                        `json:"createdAt,omitempty"`
	UpdatedAt    string                        `json:"updatedAt,omitempty"`
}

type ResponseInspectionPolicyWriteInput struct {
	ID           string
	Name         string
	Enabled      bool
	Priority     int
	ScopeType    string
	ProtocolCode string
	ProviderCode *string
	Match        ResponseInspectionPolicyMatch
	Action       string
	Notes        *string
	CreatedAt    string
	UpdatedAt    string
}

type ResponseInspectionPolicyStore interface {
	ListResponseInspectionPolicies(ctx context.Context, limit int) ([]ResponseInspectionPolicy, error)
	ResponseInspectionPolicyInTx(ctx context.Context, fn func(context.Context, ResponseInspectionPolicyTxStore) error) error
}

type ResponseInspectionPolicyTxStore interface {
	CountResponseInspectionPolicies(ctx context.Context, limit int) (int, error)
	ResponseInspectionProviderSupportsProtocol(ctx context.Context, providerCode string, protocolCode string) (bool, error)
	FindResponseInspectionPolicyForUpdate(ctx context.Context, id string) (ResponseInspectionPolicy, bool, error)
	CreateResponseInspectionPolicy(ctx context.Context, input ResponseInspectionPolicyWriteInput) (ResponseInspectionPolicy, error)
	UpdateResponseInspectionPolicy(ctx context.Context, input ResponseInspectionPolicyWriteInput) (ResponseInspectionPolicy, bool, error)
	DeleteResponseInspectionPolicy(ctx context.Context, id string) (bool, error)
}
