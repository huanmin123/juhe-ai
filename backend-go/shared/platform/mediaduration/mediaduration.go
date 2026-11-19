// Package mediaduration 提供纯 Go 的音频/视频媒体时长解析
// （docs/functions/非对话媒体统一计费设计.md §8）。
//
// 解析器只读取字节，不执行媒体内容、不使用 cgo、不调用 ffprobe/ffmpeg/mediainfo
// 等外部工具，也不引入第三方依赖。Parse 面向完整字节切片，Meter 面向流式透传，
// ParseSegments 面向远程对象的有界头/尾片段：三者共享同一套格式判定与容器解析
// 逻辑，保证同一产物在不同取证路径上得到一致的秒数。
//
// 内存边界：
//   - 头部状态不超过 64KB；
//   - MP4/WebM 的尾环/缓冲不超过 4MB；
//   - MP3/AAC/OGG/FLAC 只保留帧计数、页面头等少量状态，不随媒体总大小线性增长。
package mediaduration

import (
	"encoding/binary"
	"math"
	"mime"
	"strings"
)

// Format 是解析识别出的媒体容器/编码形态。
type Format string

const (
	FormatUnknown Format = ""
	FormatWAV     Format = "wav"
	FormatPCM     Format = "pcm"
	FormatMP3     Format = "mp3"
	FormatAAC     Format = "aac"
	FormatFLAC    Format = "flac"
	FormatOGG     Format = "ogg"
	FormatMP4     Format = "mp4"
	FormatWebM    Format = "webm"
)

// Options 是解析提示与裸 PCM 参数。
type Options struct {
	// FormatHint 来自响应 response_format 或文件扩展名，
	// 如 wav/mp3/aac/flac/ogg/opus/mp4/m4a/mov/webm/pcm。
	FormatHint string
	// ContentType 来自 HTTP Content-Type，如 audio/wav、audio/mpeg、
	// audio/L16;rate=24000、audio/mp4、video/webm。
	ContentType string
	// 裸 PCM 参数；缺省 24000Hz/1声道/16bit。ContentType 的 rate/channels 参数优先。
	SampleRate    int
	Channels      int
	BitsPerSample int
}

// Result 是一次成功解析的结果。
type Result struct {
	Seconds float64
	Format  Format
}

const (
	// maxHeaderBytes 是流式头部解析允许保留的最大字节数（设计 §8.5）。
	maxHeaderBytes = 64 << 10
	// maxTailBytes 是 MP4/WebM 尾环缓冲的最大字节数（设计 §8.5）。
	maxTailBytes = 4 << 20

	defaultSampleRate    = 24000
	defaultChannels      = 1
	defaultBitsPerSample = 16
)

// Parse 解析完整字节切片；Seconds 必须 >0 且有限才返回 ok=true。
//
// 格式判定顺序为 FormatHint、ContentType、字节嗅探；某个候选格式解析失败时
// 继续尝试后续候选，避免错误提示掩盖可识别的容器。无法识别或数据损坏/截断
// 一律返回 ok=false。Parse 不修改 data。
func Parse(data []byte, opts Options) (Result, bool) {
	if len(data) == 0 {
		return Result{}, false
	}
	for _, format := range candidateFormats(data, opts) {
		seconds, ok := parseAs(format, data, opts)
		if ok && validSeconds(seconds) {
			return Result{Seconds: seconds, Format: format}, true
		}
	}
	return Result{}, false
}

// parseAs 按指定格式解析；各解析函数只读 data。
func parseAs(format Format, data []byte, opts Options) (float64, bool) {
	switch format {
	case FormatWAV:
		return parseWAV(data)
	case FormatPCM:
		params, ok := resolvePCMParams(opts)
		if !ok {
			return 0, false
		}
		return parsePCM(data, params)
	case FormatMP3:
		return parseMP3(data)
	case FormatAAC:
		return parseAAC(data)
	case FormatFLAC:
		return parseFLAC(data)
	case FormatOGG:
		return parseOGG(data)
	case FormatMP4:
		return parseMP4(data)
	case FormatWebM:
		return parseWebM(data)
	default:
		return 0, false
	}
}

// candidateFormats 返回按优先级去重后的候选格式。
func candidateFormats(data []byte, opts Options) []Format {
	candidates := make([]Format, 0, 3)
	add := func(format Format) {
		if format == FormatUnknown {
			return
		}
		for _, existing := range candidates {
			if existing == format {
				return
			}
		}
		candidates = append(candidates, format)
	}
	add(formatFromHint(opts.FormatHint))
	add(formatFromContentType(opts.ContentType))
	add(sniffFormat(data))
	return candidates
}

// formatFromHint 把 response_format / 文件扩展名归一为 Format。
func formatFromHint(hint string) Format {
	s := strings.ToLower(strings.TrimSpace(hint))
	if s == "" {
		return FormatUnknown
	}
	if i := strings.IndexByte(s, ';'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if strings.Contains(s, "/") {
		return formatFromContentType(s)
	}
	s = strings.TrimSpace(strings.TrimPrefix(s, "."))
	switch s {
	case "wav", "wave", "x-wav", "vnd.wave", "riff":
		return FormatWAV
	case "pcm", "raw", "l8", "l16", "l24", "l32", "s16le", "s24le", "s32le":
		return FormatPCM
	case "mp3", "mp2", "mpga", "mpeg", "mpeg3":
		return FormatMP3
	case "aac", "adts", "aacp":
		return FormatAAC
	case "flac":
		return FormatFLAC
	case "ogg", "oga", "opus", "vorbis":
		return FormatOGG
	case "mp4", "m4a", "m4b", "m4v", "mov", "qt", "quicktime":
		return FormatMP4
	case "webm", "matroska", "mkv":
		return FormatWebM
	default:
		return FormatUnknown
	}
}

// formatFromContentType 把 HTTP Content-Type 归一为 Format。
func formatFromContentType(contentType string) Format {
	s := strings.TrimSpace(contentType)
	if s == "" {
		return FormatUnknown
	}
	mediaType := s
	if parsed, _, err := mime.ParseMediaType(s); err == nil {
		mediaType = parsed
	} else if i := strings.IndexByte(s, ';'); i >= 0 {
		mediaType = s[:i]
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	switch mediaType {
	case "audio/wav", "audio/x-wav", "audio/wave", "audio/vnd.wave":
		return FormatWAV
	case "audio/pcm", "audio/x-pcm", "audio/raw",
		"audio/l8", "audio/l16", "audio/l24", "audio/l32":
		return FormatPCM
	case "audio/mpeg", "audio/mp3", "audio/mpeg3", "audio/x-mpeg":
		return FormatMP3
	case "audio/aac", "audio/aacp", "audio/x-aac":
		return FormatAAC
	case "audio/flac", "audio/x-flac":
		return FormatFLAC
	case "audio/ogg", "application/ogg", "audio/opus", "audio/vorbis":
		return FormatOGG
	case "audio/mp4", "audio/x-m4a", "audio/m4a",
		"video/mp4", "video/quicktime", "video/x-m4v":
		return FormatMP4
	case "audio/webm", "video/webm":
		return FormatWebM
	default:
		return FormatUnknown
	}
}

// sniffFormat 通过文件头字节判定格式；无法可靠识别时返回 FormatUnknown。
func sniffFormat(data []byte) Format {
	switch {
	case len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WAVE":
		return FormatWAV
	case len(data) >= 4 && string(data[0:4]) == "fLaC":
		return FormatFLAC
	case len(data) >= 4 && string(data[0:4]) == "OggS":
		return FormatOGG
	case len(data) >= 8 && string(data[4:8]) == "ftyp":
		return FormatMP4
	case len(data) >= 4 && binary.BigEndian.Uint32(data[0:4]) == 0x1A45DFA3:
		return FormatWebM
	case len(data) >= 3 && string(data[0:3]) == "ID3":
		return FormatMP3
	case len(data) >= 2 && isADTSSync(data):
		return FormatAAC
	case len(data) >= 2 && isMPEGSync(data):
		return FormatMP3
	default:
		return FormatUnknown
	}
}

// isMPEGSync 判断 MPEG Audio 帧头同步位（版本、层不能为保留值）。
func isMPEGSync(b []byte) bool {
	if len(b) < 2 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return false
	}
	version := (b[1] >> 3) & 0x03
	layer := (b[1] >> 1) & 0x03
	return version != 1 && layer != 0
}

// isADTSSync 判断 ADTS 同步字：12 位 sync + layer 固定为 00。
func isADTSSync(b []byte) bool {
	return len(b) >= 2 && b[0] == 0xFF && b[1]&0xF6 == 0xF0
}

// validSeconds 统一约束成功结果的取值范围。
func validSeconds(seconds float64) bool {
	return seconds > 0 && !math.IsNaN(seconds) && !math.IsInf(seconds, 0)
}

// id3v2SkipSize 返回 ID3v2 标签头之后需要跳过的字节数（含 footer）。
func id3v2SkipSize(header []byte) (uint64, bool) {
	if len(header) < 10 || string(header[0:3]) != "ID3" {
		return 0, false
	}
	total := uint64(syncSafeUint32(header[6:10]))
	if header[5]&0x10 != 0 {
		// ID3v2.4 footer 额外 10 字节。
		total += 10
	}
	return total, true
}

// skipID3v2 返回跳过 ID3v2 标签后的偏移；标签声明长度越界视为截断。
func skipID3v2(data []byte) (int, bool) {
	if len(data) < 3 || string(data[0:3]) != "ID3" {
		return 0, true
	}
	if len(data) < 10 {
		return 0, false
	}
	size, ok := id3v2SkipSize(data)
	if !ok {
		return 0, false
	}
	total := uint64(10) + size
	if total > uint64(len(data)) {
		return 0, false
	}
	return int(total), true
}

// syncSafeUint32 解析 ID3 的 7-bit syncsafe 整数。
func syncSafeUint32(b []byte) uint32 {
	if len(b) < 4 {
		return 0
	}
	return uint32(b[0]&0x7F)<<21 |
		uint32(b[1]&0x7F)<<14 |
		uint32(b[2]&0x7F)<<7 |
		uint32(b[3]&0x7F)
}

// normalizeSampleRate / normalizeChannels / normalizeBits 把零值视为缺省，
// 负值视为非法参数。
func normalizeSampleRate(v int) (int, bool) {
	switch {
	case v == 0:
		return defaultSampleRate, true
	case v < 0 || v > 10000000:
		return 0, false
	default:
		return v, true
	}
}

func normalizeChannels(v int) (int, bool) {
	switch {
	case v == 0:
		return defaultChannels, true
	case v < 0 || v > 1024:
		return 0, false
	default:
		return v, true
	}
}

func normalizeBits(v int) (int, bool) {
	switch {
	case v == 0:
		return defaultBitsPerSample, true
	case v < 0 || v > 64:
		return 0, false
	default:
		return v, true
	}
}
