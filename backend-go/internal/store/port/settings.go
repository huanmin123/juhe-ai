package port

import (
	"context"
	"time"

	"juhe-ai/backend-go/internal/systemsettings"
)

type PublicGlobalSettings struct {
	AppName string
	AppIcon string
}

type PublicSettingsReader interface {
	PublicGlobalSettings(ctx context.Context) (PublicGlobalSettings, error)
}

type ManagementGlobalSettingsUpdateInput struct {
	AppName   *string
	AppIcon   *string
	UpdatedAt time.Time
}

type ManagementGlobalSettingsUpdateResult struct {
	Before   PublicGlobalSettings
	Settings PublicGlobalSettings
}

type ManagementGlobalSettingsWriter interface {
	UpdateGlobalSettings(ctx context.Context, input ManagementGlobalSettingsUpdateInput) (ManagementGlobalSettingsUpdateResult, error)
}

type ManagementSystemSettingsUpdateInput struct {
	Patch     systemsettings.Patch
	UpdatedAt time.Time
}

type ManagementSystemSettingsUpdateResult struct {
	Before   systemsettings.Snapshot
	Settings systemsettings.Snapshot
}

type ManagementSystemSettingsReader interface {
	ManagementSystemSettings(ctx context.Context) (systemsettings.Snapshot, error)
}

type ManagementSystemSettingsWriter interface {
	UpdateManagementSystemSettings(ctx context.Context, input ManagementSystemSettingsUpdateInput) (ManagementSystemSettingsUpdateResult, error)
}

type SystemAPIRateLimitSettings struct {
	IPReadPerMinute          int
	IPReadBurstPer10Seconds  int
	IPWritePerMinute         int
	IPWriteBurstPer10Seconds int
	UserReadPerMinute        int
	UserWritePerMinute       int
}

type SystemAPIRateLimitReader interface {
	SystemAPIRateLimitSettings(ctx context.Context) (SystemAPIRateLimitSettings, error)
}

type SystemAPIClientIPAllowlistPolicy struct {
	ID        string
	ExpiresAt *time.Time
}

type SystemAPIClientIPAllowlistReader interface {
	FindSystemAPIClientIPAllowlistPolicy(
		ctx context.Context,
		ipHash string,
		now time.Time,
	) (SystemAPIClientIPAllowlistPolicy, bool, error)
}
