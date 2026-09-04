package gatewayproto

// ParsedUsage mirrors the Node ParsedUsage contract
// (backend/src/modules/gateway/usage/types.ts). Every field is optional;
// a zero ParsedUsage means "no usage evidence observed".
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
		value.OutputImageCount != nil
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
