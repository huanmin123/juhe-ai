package port

import "context"

type PublicGlobalSettings struct {
	AppName string
	AppIcon string
}

type PublicSettingsReader interface {
	PublicGlobalSettings(ctx context.Context) (PublicGlobalSettings, error)
}

type SystemAPIIPReadRateLimitSettings struct {
	PerMinute         int
	BurstPer10Seconds int
}

type SystemAPIIPRateLimitReader interface {
	SystemAPIIPReadRateLimitSettings(ctx context.Context) (SystemAPIIPReadRateLimitSettings, error)
}
