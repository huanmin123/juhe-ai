package mediaduration

// AAC ADTS 时长解析：按帧头 frame_length 累计帧数，每帧 1024×(blocks+1) 样本。

const (
	// aacScratchSize 覆盖 ADTS 最大帧长 8191 字节。
	aacScratchSize = 16 << 10
)

var aacSampleRateTable = [13]int{
	96000, 88200, 64000, 48000, 44100, 32000,
	24000, 22050, 16000, 12000, 11025, 8000, 7350,
}

// aacFrameHeader 是解析后的 ADTS 帧头。
type aacFrameHeader struct {
	sampleRate int
	frameLen   int
	headerLen  int
	samples    int
}

// parseADTSFrameHeader 解析 7 或 9 字节 ADTS 帧头。
func parseADTSFrameHeader(b []byte) (aacFrameHeader, bool) {
	if len(b) < 7 || b[0] != 0xFF || b[1]&0xF6 != 0xF0 {
		return aacFrameHeader{}, false
	}
	sampleRateIndex := int((b[2] >> 2) & 0x0F)
	if sampleRateIndex > 12 {
		return aacFrameHeader{}, false
	}
	headerLen := 7
	if b[1]&0x01 == 0 {
		headerLen = 9
	}
	if len(b) < headerLen {
		return aacFrameHeader{}, false
	}
	frameLen := int(b[3]&0x03)<<11 | int(b[4])<<3 | int(b[5]>>5)
	if frameLen < headerLen || frameLen > 8191 {
		return aacFrameHeader{}, false
	}
	blocks := int(b[6]&0x03) + 1
	return aacFrameHeader{
		sampleRate: aacSampleRateTable[sampleRateIndex],
		frameLen:   frameLen,
		headerLen:  headerLen,
		samples:    1024 * blocks,
	}, true
}

// aacHeaderLength 返回当前前缀对应的 ADTS 头长度；0 表示同步字非法。
func aacHeaderLength(b []byte) int {
	if len(b) < 2 || b[0] != 0xFF || b[1]&0xF6 != 0xF0 {
		return 0
	}
	if b[1]&0x01 == 0 {
		return 9
	}
	return 7
}

// findADTSSync 从 start 开始寻找下一个合法 ADTS 帧头。
func findADTSSync(data []byte, start int) int {
	for i := start; i+7 <= len(data); i++ {
		if _, ok := parseADTSFrameHeader(data[i:]); ok {
			return i
		}
	}
	return -1
}

// parseAAC 遍历完整字节切片；末尾不足一帧的有效帧头视为截断。
func parseAAC(data []byte) (float64, bool) {
	offset, ok := skipID3v2(data)
	if !ok {
		return 0, false
	}
	frames := 0
	seconds := 0.0
	for offset < len(data) {
		needed := aacHeaderLength(data[offset:])
		if needed == 0 {
			next := findADTSSync(data, offset+1)
			if next < 0 {
				break
			}
			offset = next
			continue
		}
		if len(data)-offset < needed {
			return 0, false
		}
		header, ok := parseADTSFrameHeader(data[offset:])
		if !ok {
			next := findADTSSync(data, offset+1)
			if next < 0 {
				break
			}
			offset = next
			continue
		}
		if offset+header.frameLen > len(data) {
			return 0, false
		}
		frames++
		seconds += float64(header.samples) / float64(header.sampleRate)
		offset += header.frameLen
	}
	if frames == 0 {
		return 0, false
	}
	if offset < len(data) && data[offset] == 0xFF {
		// 末尾残留同步字节，帧头不完整。
		return 0, false
	}
	return seconds, seconds > 0
}

// aacMeter 是流式 ADTS 帧扫描器：scratch 只覆盖未处理的最多一帧。
type aacMeter struct {
	scratch   []byte
	probe     []byte
	probeDone bool
	skip      int64
	frames    int
	seconds   float64
	failed    bool
}

func newAACMeter() *aacMeter {
	return &aacMeter{scratch: make([]byte, 0, aacScratchSize)}
}

func (m *aacMeter) Write(p []byte) {
	if m.failed {
		return
	}
	if !m.probeDone {
		take := min(len(p), 10-len(m.probe))
		m.probe = append(m.probe, p[:take]...)
		p = p[take:]
		if len(m.probe) < 3 {
			return
		}
		if string(m.probe[0:3]) != "ID3" {
			m.probeDone = true
			pending := m.probe
			m.probe = nil
			m.process(pending)
		} else if len(m.probe) < 10 {
			return
		} else {
			skip, ok := id3v2SkipSize(m.probe)
			if !ok {
				m.failed = true
				return
			}
			m.probeDone = true
			m.probe = nil
			m.skip = int64(skip)
		}
	}
	if m.skip > 0 {
		n := int64(len(p))
		if n > m.skip {
			n = m.skip
		}
		p = p[n:]
		m.skip -= n
	}
	if len(p) > 0 {
		m.process(p)
	}
}

func (m *aacMeter) process(p []byte) {
	for len(p) > 0 && !m.failed {
		if len(m.scratch) == cap(m.scratch) {
			m.drop(1)
		}
		space := cap(m.scratch) - len(m.scratch)
		take := len(p)
		if take > space {
			take = space
		}
		m.scratch = append(m.scratch, p[:take]...)
		p = p[take:]
		m.consumeFrames()
		if len(p) > 0 && len(m.scratch) == cap(m.scratch) {
			m.drop(1)
			m.consumeFrames()
		}
	}
}

func (m *aacMeter) drop(n int) {
	if n >= len(m.scratch) {
		m.scratch = m.scratch[:0]
		return
	}
	m.scratch = append(m.scratch[:0], m.scratch[n:]...)
}

func (m *aacMeter) consumeFrames() {
	offset := 0
	for offset+2 <= len(m.scratch) {
		needed := aacHeaderLength(m.scratch[offset:])
		if needed == 0 {
			offset++
			continue
		}
		if len(m.scratch)-offset < needed {
			break
		}
		header, ok := parseADTSFrameHeader(m.scratch[offset:])
		if !ok {
			offset++
			continue
		}
		if len(m.scratch)-offset < header.frameLen {
			break
		}
		m.frames++
		m.seconds += float64(header.samples) / float64(header.sampleRate)
		offset += header.frameLen
	}
	if offset > 0 {
		m.drop(offset)
	}
}

func (m *aacMeter) Duration() (float64, bool) {
	if m.failed {
		return 0, false
	}
	m.consumeFrames()
	if m.frames == 0 {
		return 0, false
	}
	if len(m.scratch) > 0 && m.scratch[0] == 0xFF {
		// 流末尾残留同步字节但帧头/帧数据不完整。
		return 0, false
	}
	return m.seconds, m.seconds > 0
}
