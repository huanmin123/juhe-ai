package gatewayproto

// ParsedUsage mirrors the Node ParsedUsage contract
// (backend/src/modules/gateway/usage/types.ts). Every field is optional;
// a zero ParsedUsage means "no usage evidence observed".
// M1 同步音频计量扩展（音频设计 §10/契约 §2.8）：TtsInputChars 是 TTS 请求
// 输入字符数（网关自算，UTF-8 rune 数）；AudioInputSeconds 是 STT 音频秒数
// （上游 verbose_json duration）；UsageMissing 标记上游既无 usage 回报也无
// duration 的 STT 响应（0 计费 + 可审计，不猜测）。
// M2 视频计量扩展（媒体设计 §10/契约 §2.8）：OutputVideoSeconds 是视频任务
// 输出秒数（任务参数 seconds 口径，网关自算；失败任务不虚计）。指针语义沿
// M1 先例：nil = 无证据。
type ParsedUsage struct {
	UpstreamResponseModel string
	ServiceTier           string
	InputTokens           *int
	OutputTokens          *int
	CacheReadTokens       *int
	CacheWriteTokens      *int
	CacheWrite1hTokens    *int
	ThinkingTokens        *int
	InputImageTokens      *int
	OutputImageTokens     *int
	InputAudioTokens      *int
	OutputAudioTokens     *int
	OutputImageCount      *int
	TtsInputChars         *int64
	AudioInputSeconds     *float64
	OutputVideoSeconds    *float64
	UsageMissing          bool
}

// EmptyUsage returns the zero-evidence usage value.
func EmptyUsage() ParsedUsage { return ParsedUsage{} }

// Token returns the value behind an optional token count.
func Token(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

// IntToken boxes a token count.
func IntToken(value int) *int { return &value }

// MergeUsage mirrors mergeUsage: next wins whenever it carries a value.
func MergeUsage(current, next ParsedUsage) ParsedUsage {
	return ParsedUsage{
		UpstreamResponseModel: orString(next.UpstreamResponseModel, current.UpstreamResponseModel),
		ServiceTier:           orString(next.ServiceTier, current.ServiceTier),
		InputTokens:           orInt(next.InputTokens, current.InputTokens),
		OutputTokens:          orInt(next.OutputTokens, current.OutputTokens),
		CacheReadTokens:       orInt(next.CacheReadTokens, current.CacheReadTokens),
		CacheWriteTokens:      orInt(next.CacheWriteTokens, current.CacheWriteTokens),
		CacheWrite1hTokens:    orInt(next.CacheWrite1hTokens, current.CacheWrite1hTokens),
		ThinkingTokens:        orInt(next.ThinkingTokens, current.ThinkingTokens),
		InputImageTokens:      orInt(next.InputImageTokens, current.InputImageTokens),
		OutputImageTokens:     orInt(next.OutputImageTokens, current.OutputImageTokens),
		InputAudioTokens:      orInt(next.InputAudioTokens, current.InputAudioTokens),
		OutputAudioTokens:     orInt(next.OutputAudioTokens, current.OutputAudioTokens),
		OutputImageCount:      orInt(next.OutputImageCount, current.OutputImageCount),
		TtsInputChars:         orInt64(next.TtsInputChars, current.TtsInputChars),
		AudioInputSeconds:     orFloat64(next.AudioInputSeconds, current.AudioInputSeconds),
		OutputVideoSeconds:    orFloat64(next.OutputVideoSeconds, current.OutputVideoSeconds),
		UsageMissing:          current.UsageMissing || next.UsageMissing,
	}
}

// HasAnyUsageValue mirrors hasAnyUsageValue.
func HasAnyUsageValue(value ParsedUsage) bool {
	return value.ServiceTier != "" ||
		value.InputTokens != nil ||
		value.OutputTokens != nil ||
		value.CacheReadTokens != nil ||
		value.CacheWriteTokens != nil ||
		value.CacheWrite1hTokens != nil ||
		value.ThinkingTokens != nil ||
		value.InputImageTokens != nil ||
		value.OutputImageTokens != nil ||
		value.InputAudioTokens != nil ||
		value.OutputAudioTokens != nil ||
		value.OutputImageCount != nil ||
		value.TtsInputChars != nil ||
		value.AudioInputSeconds != nil ||
		value.OutputVideoSeconds != nil
}

func orString(next, current string) string {
	if next != "" {
		return next
	}
	return current
}

func orInt(next, current *int) *int {
	if next != nil {
		return next
	}
	return current
}

func orInt64(next, current *int64) *int64 {
	if next != nil {
		return next
	}
	return current
}

func orFloat64(next, current *float64) *float64 {
	if next != nil {
		return next
	}
	return current
}
