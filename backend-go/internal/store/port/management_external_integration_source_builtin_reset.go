package port

import (
	"context"
	"errors"
	"time"
)

var ErrManagementExternalIntegrationSourceBuiltInResetNotFound = errors.New(
	"management external integration source built-in reset target not found",
)

type ManagementExternalIntegrationSourceBuiltInResetInput struct {
	TokenHash            string
	TokenSecretEncrypted string
	TokenPrefix          string
	TokenSuffix          string
	UpdatedAt            time.Time
}

type ManagementExternalIntegrationSourceBuiltInResetResult struct {
	OldTokenHash string
	Source       ManagementExternalIntegrationSourceListRow
	Token        ManagementExternalIntegrationSourcePrimaryTokenRow
}

type ManagementExternalIntegrationSourceBuiltInResetter interface {
	ResetManagementExternalIntegrationSourceBuiltInToken(
		ctx context.Context,
		input ManagementExternalIntegrationSourceBuiltInResetInput,
	) (ManagementExternalIntegrationSourceBuiltInResetResult, error)
}
