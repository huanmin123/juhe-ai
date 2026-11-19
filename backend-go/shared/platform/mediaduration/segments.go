package mediaduration

import "sort"

// Segment 是远程媒体对象的一段连续字节及其在完整对象中的起始偏移。
//
// P4 视频产物探测只下载头段（前 2MB）和尾段（后 4MB），不会把远程对象
// 完整落到内存。ParseSegments 用这两个有界片段解析容器元数据，语义与
// Parse 保持一致：只读字节、不执行媒体、不依赖外部工具。
type Segment struct {
	Offset int64
	Data   []byte
}

// ParseSegments 在完整对象的若干有界片段上解析时长。
//
// 支持范围：
//   - 片段覆盖完整对象时，退化为 Parse（任意已支持格式）；
//   - MP4/MOV/M4A：在任一片段中查找 moov/mvhd（支持 faststart 头置与
//     moov 尾置两种常见布局）；
//   - WebM/Matroska：在头部片段读取 Info/Duration；缺失时用尾段最后一个
//     Cluster 的 Timecode + 块相对时间兜底。
//
// 其它格式的时长分布在完整数据上，片段不完整时返回 ok=false。任何片段
// 越界、总长度非法或数据损坏都失败关闭，不猜测。
func ParseSegments(segments []Segment, totalSize int64, opts Options) (Result, bool) {
	if totalSize <= 0 || len(segments) == 0 {
		return Result{}, false
	}
	normalized := normalizeSegments(segments, totalSize)
	if len(normalized) == 0 {
		return Result{}, false
	}
	if data, ok := completeSegmentData(normalized, totalSize); ok {
		return Parse(data, opts)
	}

	format := formatFromHint(opts.FormatHint)
	if format == FormatUnknown {
		format = formatFromContentType(opts.ContentType)
	}
	if format == FormatUnknown {
		format = sniffFormat(normalized[0].Data)
	}

	switch format {
	case FormatMP4:
		for _, segment := range normalized {
			if seconds, ok := scanMP4Moov(segment.Data); ok && validSeconds(seconds) {
				return Result{Seconds: seconds, Format: FormatMP4}, true
			}
		}
	case FormatWebM:
		scale := uint64(ebmlDefaultTimecodeScale)
		for _, segment := range normalized {
			parsedScale, duration, hasDuration, found := scanWebMInfo(segment.Data)
			if !found {
				continue
			}
			if parsedScale > 0 {
				scale = parsedScale
			}
			if hasDuration && validSeconds(duration) {
				return Result{Seconds: duration, Format: FormatWebM}, true
			}
		}
		// 尾段优先：最后一个 Cluster 的时间码代表最接近结尾的位置。
		for i := len(normalized) - 1; i >= 0; i-- {
			timecode, relative, ok := scanWebMClusterTime(normalized[i].Data)
			if !ok {
				continue
			}
			seconds := (float64(timecode) + float64(relative)) * float64(scale) / 1e9
			if validSeconds(seconds) {
				return Result{Seconds: seconds, Format: FormatWebM}, true
			}
		}
	}
	return Result{}, false
}

// normalizeSegments 过滤空片段、按偏移排序，并把越过对象末尾的片段裁齐。
func normalizeSegments(segments []Segment, totalSize int64) []Segment {
	normalized := make([]Segment, 0, len(segments))
	for _, segment := range segments {
		if len(segment.Data) == 0 || segment.Offset < 0 || segment.Offset >= totalSize {
			continue
		}
		data := segment.Data
		if end := segment.Offset + int64(len(data)); end > totalSize {
			data = data[:totalSize-segment.Offset]
		}
		if len(data) == 0 {
			continue
		}
		normalized = append(normalized, Segment{Offset: segment.Offset, Data: data})
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		return normalized[i].Offset < normalized[j].Offset
	})
	return normalized
}

// completeSegmentData 在片段无空隙覆盖 [0,totalSize) 时拼出完整对象。
// 片段之间有空洞、或未从 0 开始，返回 false。
func completeSegmentData(segments []Segment, totalSize int64) ([]byte, bool) {
	expected := int64(0)
	data := make([]byte, 0)
	for _, segment := range segments {
		if segment.Offset > expected {
			return nil, false
		}
		end := segment.Offset + int64(len(segment.Data))
		if end <= expected {
			continue
		}
		start := expected - segment.Offset
		data = append(data, segment.Data[start:]...)
		expected = end
		if expected >= totalSize {
			if int64(len(data)) > totalSize {
				data = data[:totalSize]
			}
			return data, true
		}
	}
	return nil, false
}
