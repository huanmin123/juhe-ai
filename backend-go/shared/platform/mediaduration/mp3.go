package mediaduration

import (
	"encoding/binary"
)

// MP3（MPEG Audio Layer I/II/III）时长解析：
// 跳过 ID3v2，按帧头累计帧数；Xing/Info/VBRI 头只用于截断校验。

const (
	// mp3ScratchSize 覆盖最大 MPEG Audio 帧（约 1441 字节），留足重同步余量。
	mp3ScratchSize = 8 << 10
)

// mp3FrameHeader 是解析后的 MPEG Audio 帧头。
type mp3FrameHeader struct {
	version     int // 3=MPEG1，2=MPEG2，0=MPEG2.5
	layer       int // 3=Layer I，2=Layer II，1=Layer III
	sampleRate  int
	bitrate     int // kbps
	frameLen    int
	samples     int
	sideInfo    int
	headerExtra int // CRC 占用的 2 字节
}

// 下标为 layer：1=Layer III，2=Layer II，3=Layer I。
var mpeg1BitrateTable = [4][16]int{
	1: {0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0},
	2: {0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 0},
	3: {0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448, 0},
}

var mpeg2BitrateTable = [4][16]int{
	1: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
	2: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
	3: {0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, 0},
}

var mpegSampleRateTable = [4][3]int{
	0: {11025, 12000, 8000},
	2: {22050, 24000, 16000},
	3: {44100, 48000, 32000},
}

// parseMP3FrameHeader 解析 4 字节 MPEG Audio 帧头。
func parseMP3FrameHeader(b []byte) (mp3FrameHeader, bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return mp3FrameHeader{}, false
	}
	version := int((b[1] >> 3) & 0x03)
	if version == 1 {
		return mp3FrameHeader{}, false
	}
	layer := int((b[1] >> 1) & 0x03)
	if layer == 0 {
		return mp3FrameHeader{}, false
	}
	bitrateIndex := int((b[2] >> 4) & 0x0F)
	sampleRateIndex := int((b[2] >> 2) & 0x03)
	if bitrateIndex == 0 || bitrateIndex == 15 || sampleRateIndex == 3 {
		return mp3FrameHeader{}, false
	}
	var bitrate int
	if version == 3 {
		bitrate = mpeg1BitrateTable[layer][bitrateIndex]
	} else {
		bitrate = mpeg2BitrateTable[layer][bitrateIndex]
	}
	sampleRate := mpegSampleRateTable[version][sampleRateIndex]
	if bitrate == 0 || sampleRate == 0 {
		return mp3FrameHeader{}, false
	}
	padding := int((b[2] >> 1) & 0x01)

	var samples, frameLen int
	switch layer {
	case 3: // Layer I
		samples = 384
		frameLen = (12*bitrate*1000/sampleRate + padding) * 4
	case 2: // Layer II
		samples = 1152
		frameLen = 144*bitrate*1000/sampleRate + padding
	default: // Layer III
		if version == 3 {
			samples = 1152
		} else {
			samples = 576
		}
		frameLen = samples/8*bitrate*1000/sampleRate + padding
	}
	if frameLen <= 4 {
		return mp3FrameHeader{}, false
	}

	headerExtra := 0
	if b[1]&0x01 == 0 {
		headerExtra = 2
	}
	channelMode := int((b[3] >> 6) & 0x03)
	var sideInfo int
	if version == 3 {
		if channelMode == 3 {
			sideInfo = 17
		} else {
			sideInfo = 32
		}
	} else {
		if channelMode == 3 {
			sideInfo = 9
		} else {
			sideInfo = 17
		}
	}
	return mp3FrameHeader{
		version:     version,
		layer:       layer,
		sampleRate:  sampleRate,
		bitrate:     bitrate,
		frameLen:    frameLen,
		samples:     samples,
		sideInfo:    sideInfo,
		headerExtra: headerExtra,
	}, true
}

// parseMP3VBRHeader 读取 Xing/Info 或 VBRI 的帧数。
func parseMP3VBRHeader(frame []byte, header mp3FrameHeader) (int, bool) {
	xingOffset := 4 + header.headerExtra + header.sideInfo
	if xingOffset+8 <= len(frame) {
		tag := string(frame[xingOffset : xingOffset+4])
		if tag == "Xing" || tag == "Info" {
			flags := binary.BigEndian.Uint32(frame[xingOffset+4 : xingOffset+8])
			if flags&0x01 != 0 && xingOffset+12 <= len(frame) {
				frames := int(binary.BigEndian.Uint32(frame[xingOffset+8 : xingOffset+12]))
				if frames > 0 {
					return frames, true
				}
			}
		}
	}
	if 36+18 <= len(frame) && string(frame[36:40]) == "VBRI" {
		frames := int(binary.BigEndian.Uint32(frame[36+14 : 36+18]))
		if frames > 0 {
			return frames, true
		}
	}
	return 0, false
}

// findMP3Sync 从 start 开始寻找下一个合法 MPEG Audio 帧头。
func findMP3Sync(data []byte, start int) int {
	for i := start; i+4 <= len(data); i++ {
		if _, ok := parseMP3FrameHeader(data[i:]); ok {
			return i
		}
	}
	return -1
}

// parseMP3 遍历完整字节切片；末尾不足一帧的有效帧头视为截断。
func parseMP3(data []byte) (float64, bool) {
	offset, ok := skipID3v2(data)
	if !ok {
		return 0, false
	}
	frames := 0
	declaredFrames := 0
	haveDeclared := false
	seconds := 0.0
	for offset < len(data) {
		if len(data)-offset == 128 && string(data[offset:offset+3]) == "TAG" {
			break
		}
		header, ok := parseMP3FrameHeader(data[offset:])
		if !ok {
			next := findMP3Sync(data, offset+1)
			if next < 0 {
				break
			}
			offset = next
			continue
		}
		if offset+header.frameLen > len(data) {
			return 0, false
		}
		if frames == 0 {
			if declared, ok := parseMP3VBRHeader(data[offset:offset+header.frameLen], header); ok {
				declaredFrames = declared
				haveDeclared = true
			}
		}
		frames++
		seconds += float64(header.samples) / float64(header.sampleRate)
		offset += header.frameLen
	}
	if frames == 0 {
		return 0, false
	}
	if haveDeclared && frames < declaredFrames {
		return 0, false
	}
	if offset < len(data) && data[offset] == 0xFF {
		// 末尾残留同步字节，帧头不完整。
		return 0, false
	}
	return seconds, seconds > 0
}

// mp3Meter 是流式 MP3 帧扫描器：scratch 只覆盖未处理的最多一帧。
type mp3Meter struct {
	scratch        []byte
	probe          []byte
	probeDone      bool
	skip           int64
	frames         int
	seconds        float64
	declaredFrames int
	haveDeclared   bool
	failed         bool
}

func newMP3Meter() *mp3Meter {
	return &mp3Meter{scratch: make([]byte, 0, mp3ScratchSize)}
}

func (m *mp3Meter) Write(p []byte) {
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

// process 用固定容量的 scratch 消化输入，避免临时缓冲随写入块大小增长。
func (m *mp3Meter) process(p []byte) {
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
			// 缓冲已满且没有消费完整帧：丢弃一个字节重新同步。
			m.drop(1)
			m.consumeFrames()
		}
	}
}

// drop 从 scratch 头部丢弃 n 字节，原地压缩保留剩余数据。
func (m *mp3Meter) drop(n int) {
	if n >= len(m.scratch) {
		m.scratch = m.scratch[:0]
		return
	}
	m.scratch = append(m.scratch[:0], m.scratch[n:]...)
}

// consumeFrames 消费 scratch 中所有完整帧；遇到非法字节只记录偏移，
// 单次扫描结束后统一丢弃前缀，避免逐字节搬移 8KB 缓冲。
func (m *mp3Meter) consumeFrames() {
	offset := 0
	for offset+4 <= len(m.scratch) {
		if string(m.scratch[offset:offset+3]) == "TAG" {
			// ID3v1 尾部标签，忽略其后内容。
			offset = len(m.scratch)
			break
		}
		header, ok := parseMP3FrameHeader(m.scratch[offset:])
		if !ok {
			offset++
			continue
		}
		if len(m.scratch)-offset < header.frameLen {
			break
		}
		if m.frames == 0 {
			if declared, ok := parseMP3VBRHeader(m.scratch[offset:offset+header.frameLen], header); ok {
				m.declaredFrames = declared
				m.haveDeclared = true
			}
		}
		m.frames++
		m.seconds += float64(header.samples) / float64(header.sampleRate)
		offset += header.frameLen
	}
	if offset > 0 {
		m.drop(offset)
	}
}

func (m *mp3Meter) Duration() (float64, bool) {
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
	if m.haveDeclared && m.frames < m.declaredFrames {
		return 0, false
	}
	return m.seconds, m.seconds > 0
}
