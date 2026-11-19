package mediaduration

import (
	"encoding/binary"
)

// OGG 时长解析：读取页面 granule position；Opus 按 48kHz 扣 pre-skip，
// Vorbis 使用 identification 头中的采样率。

const (
	oggCodecUnknown = iota
	oggCodecOpus
	oggCodecVorbis
)

const (
	// oggGranuleNone 表示页面尚未完成任何包（-1）。
	oggGranuleNone = ^uint64(0)
	// oggIDMax 是识别 Opus/Vorbis 头所需的最大前缀字节数。
	oggIDMax = 64
	// oggOpusSampleRate 是 Opus granule 的固定时间基准。
	oggOpusSampleRate = 48000
)

// oggPageHeader 是解析后的 OGG 页面头。
type oggPageHeader struct {
	headerType byte
	granule    uint64
	serial     uint32
	headerLen  int
	bodyLen    int
}

func parseOggPageHeader(data []byte) (oggPageHeader, bool) {
	if len(data) < 27 || string(data[0:4]) != "OggS" || data[4] != 0 {
		return oggPageHeader{}, false
	}
	segments := int(data[26])
	headerLen := 27 + segments
	if len(data) < headerLen {
		return oggPageHeader{}, false
	}
	bodyLen := 0
	for _, lacing := range data[27:headerLen] {
		bodyLen += int(lacing)
	}
	return oggPageHeader{
		headerType: data[5],
		granule:    binary.LittleEndian.Uint64(data[6:14]),
		serial:     binary.LittleEndian.Uint32(data[14:18]),
		headerLen:  headerLen,
		bodyLen:    bodyLen,
	}, true
}

// identifyOggCodec 从第一页 body（identification 包头）识别 Opus/Vorbis。
func identifyOggCodec(body []byte) (codec int, preSkip uint16, sampleRate uint32) {
	if len(body) >= 8 && string(body[0:8]) == "OpusHead" {
		if len(body) >= 12 {
			return oggCodecOpus, binary.LittleEndian.Uint16(body[10:12]), oggOpusSampleRate
		}
		return oggCodecUnknown, 0, 0
	}
	if len(body) >= 7 && body[0] == 0x01 && string(body[1:7]) == "vorbis" {
		if len(body) >= 16 {
			return oggCodecVorbis, 0, binary.LittleEndian.Uint32(body[12:16])
		}
	}
	return oggCodecUnknown, 0, 0
}

// parseOGG 严格遍历页面；任何页面头或 body 越界都视为截断。
func parseOGG(data []byte) (float64, bool) {
	offset := 0
	first := true
	haveSerial := false
	var serial uint32
	codec := oggCodecUnknown
	var preSkip uint16
	var sampleRate uint32
	lastGranule := uint64(0)
	sawGranule := false

	for offset < len(data) {
		header, ok := parseOggPageHeader(data[offset:])
		if !ok {
			return 0, false
		}
		bodyStart := offset + header.headerLen
		bodyEnd := bodyStart + header.bodyLen
		if bodyEnd > len(data) {
			return 0, false
		}
		if first {
			serial = header.serial
			haveSerial = true
			codec, preSkip, sampleRate = identifyOggCodec(data[bodyStart:bodyEnd])
			first = false
		}
		if haveSerial && header.serial == serial && header.granule != oggGranuleNone {
			lastGranule = header.granule
			sawGranule = true
		}
		offset = bodyEnd
	}

	if codec == oggCodecUnknown || !sawGranule {
		return 0, false
	}
	switch codec {
	case oggCodecOpus:
		if lastGranule <= uint64(preSkip) {
			return 0, false
		}
		seconds := float64(lastGranule-uint64(preSkip)) / oggOpusSampleRate
		return seconds, seconds > 0
	default:
		if sampleRate == 0 || lastGranule == 0 {
			return 0, false
		}
		seconds := float64(lastGranule) / float64(sampleRate)
		return seconds, seconds > 0
	}
}

// oggMeter 是流式 OGG 页面解析器：只保留页面头与首包识别前缀。
type oggMeter struct {
	header      []byte
	bodyRemain  int
	firstPage   bool
	haveSerial  bool
	serial      uint32
	codec       int
	preSkip     uint16
	sampleRate  uint32
	idBuf       []byte
	lastGranule uint64
	sawGranule  bool
	failed      bool
}

func newOGGMeter() *oggMeter {
	return &oggMeter{
		header:    make([]byte, 0, 27+255),
		firstPage: true,
	}
}

func (m *oggMeter) Write(p []byte) {
	if m.failed {
		return
	}
	for len(p) > 0 {
		if m.bodyRemain > 0 {
			n := len(p)
			if n > m.bodyRemain {
				n = m.bodyRemain
			}
			if m.firstPage && len(m.idBuf) < oggIDMax {
				take := min(n, oggIDMax-len(m.idBuf))
				m.idBuf = append(m.idBuf, p[:take]...)
				m.detectCodec()
			}
			p = p[n:]
			m.bodyRemain -= n
			if m.bodyRemain == 0 {
				m.firstPage = false
			}
			continue
		}

		if len(m.header) < 27 {
			take := min(len(p), 27-len(m.header))
			m.header = append(m.header, p[:take]...)
			p = p[take:]
			if len(m.header) < 27 {
				return
			}
			continue
		}
		need := 27 + int(m.header[26])
		take := min(len(p), need-len(m.header))
		m.header = append(m.header, p[:take]...)
		p = p[take:]
		if len(m.header) < need {
			return
		}
		if string(m.header[0:4]) != "OggS" || m.header[4] != 0 {
			m.failed = true
			return
		}
		segments := int(m.header[26])
		bodyLen := 0
		for _, lacing := range m.header[27 : 27+segments] {
			bodyLen += int(lacing)
		}
		granule := binary.LittleEndian.Uint64(m.header[6:14])
		serial := binary.LittleEndian.Uint32(m.header[14:18])
		if !m.haveSerial {
			m.serial = serial
			m.haveSerial = true
		}
		if serial == m.serial && granule != oggGranuleNone {
			m.lastGranule = granule
			m.sawGranule = true
		}
		m.header = m.header[:0]
		m.bodyRemain = bodyLen
		if bodyLen == 0 {
			m.firstPage = false
		}
	}
}

func (m *oggMeter) detectCodec() {
	if m.codec != oggCodecUnknown {
		return
	}
	if len(m.idBuf) >= 8 && string(m.idBuf[0:8]) == "OpusHead" {
		if len(m.idBuf) >= 12 {
			m.codec = oggCodecOpus
			m.preSkip = binary.LittleEndian.Uint16(m.idBuf[10:12])
			m.sampleRate = oggOpusSampleRate
		}
		return
	}
	if len(m.idBuf) >= 7 && m.idBuf[0] == 0x01 && string(m.idBuf[1:7]) == "vorbis" {
		if len(m.idBuf) >= 16 {
			m.codec = oggCodecVorbis
			m.sampleRate = binary.LittleEndian.Uint32(m.idBuf[12:16])
		}
	}
}

func (m *oggMeter) Duration() (float64, bool) {
	if m.failed || m.bodyRemain != 0 || len(m.header) != 0 || !m.sawGranule {
		return 0, false
	}
	switch m.codec {
	case oggCodecOpus:
		if m.lastGranule <= uint64(m.preSkip) {
			return 0, false
		}
		seconds := float64(m.lastGranule-uint64(m.preSkip)) / oggOpusSampleRate
		return seconds, seconds > 0
	case oggCodecVorbis:
		if m.sampleRate == 0 || m.lastGranule == 0 {
			return 0, false
		}
		seconds := float64(m.lastGranule) / float64(m.sampleRate)
		return seconds, seconds > 0
	default:
		return 0, false
	}
}
