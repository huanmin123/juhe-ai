package gatewayrouting

import "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"

// GatewayTimeoutSettings mirrors policy/timeout-profile.ts
// GatewayTimeoutSettings (values arrive as integers from the settings
// bounds: min 1, max 3600 seconds).
type GatewayTimeoutSettings struct {
	TextFirstResponseTimeoutSeconds           int64
	TextNonStreamFirstResponseTimeoutSeconds  int64
	TextStreamIdleTimeoutSeconds              int64
	TextUncommittedAttemptMaxLifetimeSeconds  int64
	ImageFirstResponseTimeoutSeconds          int64
	ImageStreamIdleTimeoutSeconds             int64
	ImageUncommittedAttemptMaxLifetimeSeconds int64
	// 媒体车道独立超时档位（音频视频模型接入设计 §3）：audio 同步端点与
	// video 创建请求；两车道暂只有首响档（无独立 idle / uncommitted 键），
	// 其余超时维度沿用文本车道取值。
	AudioFirstResponseTimeoutSeconds          int64
	VideoCreateTimeoutSeconds                 int64
	NoAvailableAccountWaitTimeoutSeconds      int64
}

// GatewayTimeoutProfile mirrors GatewayTimeoutProfile. TimeoutsDisabled
// mirrors the optional `timeoutsDisabled?: true` marker.
type GatewayTimeoutProfile struct {
	TimeoutsDisabled                bool
	FirstResponseTimeoutMs          int64
	NonStreamFirstResponseTimeoutMs int64
	FirstByteTimeoutMs              int64
	IdleTimeoutMs                   int64
	UncommittedAttemptMaxLifetimeMs int64
	NoAvailableAccountWaitMs        int64
}

// GatewayTimeoutProfileForLane mirrors gatewayTimeoutProfileForLane: the
// image lane reads the image settings, the audio / video lanes read the media
// lane tiers (媒体设计 §3 车道独立超时), every other lane reads text. The
// disableTimeouts flag mirrors options.disableTimeouts === true.
func GatewayTimeoutProfileForLane(settings GatewayTimeoutSettings, lane gatewayproto.RequestLane, disableTimeouts bool) GatewayTimeoutProfile {
	firstResponseTimeoutSeconds := settings.TextFirstResponseTimeoutSeconds
	nonStreamFirstResponseSeconds := settings.TextNonStreamFirstResponseTimeoutSeconds
	idleTimeoutSeconds := settings.TextStreamIdleTimeoutSeconds
	uncommittedAttemptMaxLifetimeSeconds := settings.TextUncommittedAttemptMaxLifetimeSeconds
	if lane == gatewayproto.LaneImage {
		firstResponseTimeoutSeconds = settings.ImageFirstResponseTimeoutSeconds
		nonStreamFirstResponseSeconds = settings.ImageFirstResponseTimeoutSeconds
		idleTimeoutSeconds = settings.ImageStreamIdleTimeoutSeconds
		uncommittedAttemptMaxLifetimeSeconds = settings.ImageUncommittedAttemptMaxLifetimeSeconds
	}
	if lane == gatewayproto.LaneAudio {
		// audio 同步端点首响即首字节（二进制流式透传），非流式口径同档；
		// idle / uncommitted 沿用文本车道取值（无独立键）。
		firstResponseTimeoutSeconds = settings.AudioFirstResponseTimeoutSeconds
		nonStreamFirstResponseSeconds = settings.AudioFirstResponseTimeoutSeconds
	}
	if lane == gatewayproto.LaneVideo {
		// video 创建请求是小 JSON，应快速返回 job 对象；任务轮询/产物下载
		// 不走派发循环（媒体设计 §7），不消费该档位。
		firstResponseTimeoutSeconds = settings.VideoCreateTimeoutSeconds
		nonStreamFirstResponseSeconds = settings.VideoCreateTimeoutSeconds
	}

	return GatewayTimeoutProfile{
		TimeoutsDisabled:                disableTimeouts,
		FirstResponseTimeoutMs:          secondsToMilliseconds(firstResponseTimeoutSeconds),
		NonStreamFirstResponseTimeoutMs: secondsToMilliseconds(nonStreamFirstResponseSeconds),
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
