package mediaduration

import "io"

// streamParser 是流式格式解析器的最小接口。
type streamParser interface {
	Write(p []byte)
	Duration() (float64, bool)
}

// tailBuffer 是固定容量的环形缓冲，保留最近写入的字节。
type tailBuffer struct {
	buf   []byte
	start int
	size  int
}

func newTailBuffer(capacity int) *tailBuffer {
	return &tailBuffer{buf: make([]byte, capacity)}
}

func (t *tailBuffer) write(p []byte) {
	capacity := len(t.buf)
	if capacity == 0 || len(p) == 0 {
		return
	}
	if len(p) >= capacity {
		copy(t.buf, p[len(p)-capacity:])
		t.start = 0
		t.size = capacity
		return
	}
	if t.size+len(p) > capacity {
		drop := t.size + len(p) - capacity
		t.start = (t.start + drop) % capacity
		t.size -= drop
	}
	end := (t.start + t.size) % capacity
	copied := copy(t.buf[end:], p)
	if copied < len(p) {
		copy(t.buf, p[copied:])
	}
	t.size += len(p)
}

func (t *tailBuffer) bytes() []byte {
	if t.size == 0 {
		return nil
	}
	// 未回绕时直接返回底层切片，避免额外复制一份 4MB 数据。
	if t.start+t.size <= len(t.buf) {
		return t.buf[t.start : t.start+t.size]
	}
	out := make([]byte, t.size)
	first := copy(out, t.buf[t.start:])
	if first < t.size {
		copy(out[first:], t.buf[:t.size-first])
	}
	return out
}

// Meter 实现 io.Writer，累计流式响应字节，并在流结束后给出时长。
//
// 内存占用与媒体总大小无关：头部最多保留 maxHeaderBytes，MP4/WebM 另加最多
// maxTailBytes 的尾环；其它格式只保留帧/页解析状态。
type Meter struct {
	opts     Options
	format   Format
	decided  bool
	failed   bool
	head     []byte
	tail     *tailBuffer
	parser   streamParser
	buffered bool
	total    uint64
}

var _ io.Writer = (*Meter)(nil)

// NewMeter 创建流式时长计量器。格式提示与 Parse 使用同一套归一规则。
func NewMeter(opts Options) *Meter {
	meter := &Meter{opts: opts}
	if format := formatFromHint(opts.FormatHint); format != FormatUnknown {
		meter.decide(format)
	} else if format := formatFromContentType(opts.ContentType); format != FormatUnknown {
		meter.decide(format)
	}
	return meter
}

// decide 初始化指定格式的解析状态。
func (m *Meter) decide(format Format) {
	m.format = format
	m.decided = true
	switch format {
	case FormatPCM:
		params, ok := resolvePCMParams(m.opts)
		if !ok {
			m.failed = true
			return
		}
		m.parser = &pcmMeter{params: params}
	case FormatWAV:
		m.parser = newWAVMeter()
	case FormatMP3:
		m.parser = newMP3Meter()
	case FormatAAC:
		m.parser = newAACMeter()
	case FormatFLAC:
		m.parser = newFLACMeter()
	case FormatOGG:
		m.parser = newOGGMeter()
	case FormatMP4, FormatWebM:
		m.buffered = true
	default:
		m.failed = true
	}
}

// Write 实现 io.Writer；解析失败只影响 Duration，不改变转发结果。
func (m *Meter) Write(p []byte) (int, error) {
	n := len(p)
	if n == 0 {
		return 0, nil
	}
	if m.failed {
		return n, nil
	}
	if m.decided {
		m.feed(p)
		return n, nil
	}

	space := maxHeaderBytes - len(m.head)
	if space <= 0 {
		m.failed = true
		return n, nil
	}
	m.ensureHead()
	take := min(n, space)
	m.head = append(m.head, p[:take]...)
	if format := sniffFormat(m.head); format != FormatUnknown {
		head := m.head
		m.head = nil
		m.decide(format)
		if !m.failed {
			m.feed(head)
			m.feed(p[take:])
		}
		return n, nil
	}
	if len(m.head) >= maxHeaderBytes {
		m.failed = true
		m.head = nil
	}
	return n, nil
}

// ensureHead 懒分配固定容量的头部缓冲，避免 append 扩容超过 64KB 上限。
func (m *Meter) ensureHead() {
	if m.head == nil {
		m.head = make([]byte, 0, maxHeaderBytes)
	}
}

// feed 把已判定格式的字节送入对应解析状态。
func (m *Meter) feed(p []byte) {
	if len(p) == 0 || m.failed {
		return
	}
	m.total += uint64(len(p))
	if m.buffered {
		if len(m.head) < maxHeaderBytes {
			m.ensureHead()
			take := min(len(p), maxHeaderBytes-len(m.head))
			m.head = append(m.head, p[:take]...)
		}
		if m.tail == nil {
			m.tail = newTailBuffer(maxTailBytes)
		}
		m.tail.write(p)
		return
	}
	if m.parser != nil {
		m.parser.Write(p)
	}
}

// Duration 在流结束后返回时长；未识别/截断/损坏返回 ok=false。
func (m *Meter) Duration() (Result, bool) {
	if m.failed {
		return Result{}, false
	}
	if !m.decided {
		// 数据量很小、尚未触发嗅探时，退化为一次完整解析。
		if len(m.head) > 0 {
			if result, ok := Parse(m.head, m.opts); ok {
				return result, true
			}
		}
		return Result{}, false
	}

	var seconds float64
	var ok bool
	if m.buffered {
		seconds, ok = m.bufferedDuration()
	} else if m.parser != nil {
		seconds, ok = m.parser.Duration()
	}
	if !ok || !validSeconds(seconds) {
		return Result{}, false
	}
	return Result{Seconds: seconds, Format: m.format}, true
}

// bufferedDuration 在首部与尾环缓冲中查找 MP4 moov/mvhd 或 WebM Info/Cluster。
// 当整个流都能放进尾环时，直接用严格解析校验截断；否则退化为有界扫描。
func (m *Meter) bufferedDuration() (float64, bool) {
	if m.tail == nil || m.total == 0 {
		return 0, false
	}
	if m.total <= uint64(maxTailBytes) {
		all := m.tail.bytes()
		switch m.format {
		case FormatMP4:
			return parseMP4(all)
		case FormatWebM:
			return parseWebM(all)
		default:
			return 0, false
		}
	}
	switch m.format {
	case FormatMP4:
		if seconds, ok := scanMP4Moov(m.head); ok {
			return seconds, true
		}
		if m.tail != nil {
			return scanMP4Moov(m.tail.bytes())
		}
	case FormatWebM:
		scale := uint64(ebmlDefaultTimecodeScale)
		if parsedScale, duration, hasDuration, found := scanWebMInfo(m.head); found {
			scale = parsedScale
			if hasDuration {
				seconds := duration * float64(scale) / 1e9
				if validSeconds(seconds) {
					return seconds, true
				}
			}
		}
		if m.tail != nil {
			tailBytes := m.tail.bytes()
			if parsedScale, duration, hasDuration, found := scanWebMInfo(tailBytes); found {
				scale = parsedScale
				if hasDuration {
					seconds := duration * float64(scale) / 1e9
					if validSeconds(seconds) {
						return seconds, true
					}
				}
			}
			if timecode, relative, ok := scanWebMClusterTime(tailBytes); ok {
				seconds := (float64(timecode) + float64(relative)) * float64(scale) / 1e9
				if validSeconds(seconds) {
					return seconds, true
				}
			}
		}
	}
	return 0, false
}
