package gatewayrouting

import "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"

// GatewayTimeoutSettings mirrors policy/timeout-profile.ts
// GatewayTimeoutSettings (values arrive as integers from the settings
// bounds: min 1, max 3600 seconds).
type GatewayTimeoutSettings struct {
	TextFirstResponseTimeoutSeconds           int64
	TextStreamIdleTimeoutSeconds              int64
	TextUncommittedAttemptMaxLifetimeSeconds  int64
	ImageFirstResponseTimeoutSeconds          int64
	ImageStreamIdleTimeoutSeconds             int64
	ImageUncommittedAttemptMaxLifetimeSeconds int64
	NoAvailableAccountWaitTimeoutSeconds      int64
}

// GatewayTimeoutProfile mirrors GatewayTimeoutProfile. TimeoutsDisabled
// mirrors the optional `timeoutsDisabled?: true` marker.
type GatewayTimeoutProfile struct {
	TimeoutsDisabled                bool
	FirstResponseTimeoutMs          int64
	FirstByteTimeoutMs              int64
	IdleTimeoutMs                   int64
	UncommittedAttemptMaxLifetimeMs int64
	NoAvailableAccountWaitMs        int64
}

// GatewayTimeoutProfileForLane mirrors gatewayTimeoutProfileForLane: the
// image lane reads the image settings, every other lane reads text. The
// disableTimeouts flag mirrors options.disableTimeouts === true.
func GatewayTimeoutProfileForLane(settings GatewayTimeoutSettings, lane gatewayproto.RequestLane, disableTimeouts bool) GatewayTimeoutProfile {
	firstResponseTimeoutSeconds := settings.TextFirstResponseTimeoutSeconds
	idleTimeoutSeconds := settings.TextStreamIdleTimeoutSeconds
	uncommittedAttemptMaxLifetimeSeconds := settings.TextUncommittedAttemptMaxLifetimeSeconds
	if lane == gatewayproto.LaneImage {
		firstResponseTimeoutSeconds = settings.ImageFirstResponseTimeoutSeconds
		idleTimeoutSeconds = settings.ImageStreamIdleTimeoutSeconds
		uncommittedAttemptMaxLifetimeSeconds = settings.ImageUncommittedAttemptMaxLifetimeSeconds
	}

	return GatewayTimeoutProfile{
		TimeoutsDisabled:                disableTimeouts,
		FirstResponseTimeoutMs:          secondsToMilliseconds(firstResponseTimeoutSeconds),
		FirstByteTimeoutMs:              secondsToMilliseconds(firstResponseTimeoutSeconds),
		IdleTimeoutMs:                   secondsToMilliseconds(idleTimeoutSeconds),
		UncommittedAttemptMaxLifetimeMs: secondsToMilliseconds(uncommittedAttemptMaxLifetimeSeconds),
		NoAvailableAccountWaitMs:        secondsToMilliseconds(settings.NoAvailableAccountWaitTimeoutSeconds),
	}
}

// secondsToMilliseconds mirrors secondsToMilliseconds: sub-second and zero
// values clamp up to one second.
func secondsToMilliseconds(seconds int64) int64 {
	if seconds < 1 {
		seconds = 1
	}
	return seconds * 1000
}
