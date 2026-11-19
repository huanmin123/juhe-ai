package mediaduration

import (
	"mime"
	"strconv"
	"strings"
)

// 裸 PCM 时长解析：按字节数 ÷ (采样率 × 声道数 × 位深 / 8) 计算。

// pcmParams 是裸 PCM 的生效参数。
type pcmParams struct {
	sampleRate int
	channels   int
	bits       int
}

// resolvePCMParams 按 ContentType 参数 > Options > 缺省值 的优先级解析参数。
// ContentType 形如 audio/L16;rate=24000;channels=1。
func resolvePCMParams(opts Options) (pcmParams, bool) {
	params := pcmParams{
		sampleRate: opts.SampleRate,
		channels:   opts.Channels,
		bits:       opts.BitsPerSample,
	}

	if mediaType, values, err := mime.ParseMediaType(strings.TrimSpace(opts.ContentType)); err == nil {
		switch strings.ToLower(mediaType) {
		case "audio/l8":
			params.bits = 8
		case "audio/l16":
			params.bits = 16
		case "audio/l24":
			params.bits = 24
		case "audio/l32":
			params.bits = 32
		}
		if ok := applyPCMIntParam(values, &params.sampleRate, "rate", "samplerate", "sample_rate"); !ok {
			return pcmParams{}, false
		}
		if ok := applyPCMIntParam(values, &params.channels, "channels", "channel"); !ok {
			return pcmParams{}, false
		}
		if ok := applyPCMIntParam(values, &params.bits, "bits", "bitdepth", "bitspersample"); !ok {
			return pcmParams{}, false
		}
	}

	sampleRate, ok := normalizeSampleRate(params.sampleRate)
	if !ok {
		return pcmParams{}, false
	}
	channels, ok := normalizeChannels(params.channels)
	if !ok {
		return pcmParams{}, false
	}
	bits, ok := normalizeBits(params.bits)
	if !ok {
		return pcmParams{}, false
	}
	return pcmParams{sampleRate: sampleRate, channels: channels, bits: bits}, true
}

// applyPCMIntParam 从 ContentType 参数中读取正整数；出现非法值视为解析失败。
func applyPCMIntParam(values map[string]string, dst *int, keys ...string) bool {
	for _, key := range keys {
		raw, exists := values[key]
		if !exists {
			continue
		}
		v, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || v <= 0 {
			return false
		}
		*dst = v
	}
	return true
}

// parsePCM 按裸 PCM 参数计算完整字节切片的时长。
func parsePCM(data []byte, params pcmParams) (float64, bool) {
	if len(data) == 0 {
		return 0, false
	}
	bytesPerSample := float64(params.channels) * float64(params.bits) / 8
	if bytesPerSample <= 0 {
		return 0, false
	}
	seconds := float64(len(data)) / (float64(params.sampleRate) * bytesPerSample)
	return seconds, seconds > 0
}

// pcmMeter 只累计字节数，不保留任何媒体内容。
type pcmMeter struct {
	params pcmParams
	bytes  uint64
}

func (m *pcmMeter) Write(p []byte) {
	m.bytes += uint64(len(p))
}

func (m *pcmMeter) Duration() (float64, bool) {
	if m.bytes == 0 {
		return 0, false
	}
	bytesPerSample := float64(m.params.channels) * float64(m.params.bits) / 8
	if bytesPerSample <= 0 {
		return 0, false
	}
	seconds := float64(m.bytes) / (float64(m.params.sampleRate) * bytesPerSample)
	return seconds, seconds > 0
}
