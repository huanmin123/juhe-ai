package port

import "context"

type PublicGlobalSettings struct {
	AppName string
	AppIcon string
}

type PublicSettingsReader interface {
	PublicGlobalSettings(ctx context.Context) (PublicGlobalSettings, error)
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
