package port

import (
	"context"
	"time"
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
