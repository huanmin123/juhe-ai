package mediaduration

// FLAC 时长解析：读取 STREAMINFO 的 sample rate 与 total samples。

const (
	flacStatusNeedMore = iota
	flacStatusFound
	flacStatusInvalid
)

// parseFLAC 严格检查 STREAMINFO 元数据块；缺失或越界返回 false。
func parseFLAC(data []byte) (float64, bool) {
	seconds, status := parseFLACHeader(data)
	if status != flacStatusFound {
		return 0, false
	}
	return seconds, seconds > 0
}

// parseFLACHeader 在已有字节中查找 STREAMINFO；返回状态便于流式解析区分
// “需要更多数据”与“确定损坏”。
func parseFLACHeader(data []byte) (float64, int) {
	if len(data) < 4 {
		return 0, flacStatusNeedMore
	}
	if string(data[0:4]) != "fLaC" {
		return 0, flacStatusInvalid
	}
	offset := 4
	for {
		if len(data) < offset+4 {
			return 0, flacStatusNeedMore
		}
		last := data[offset]&0x80 != 0
		blockType := data[offset] & 0x7F
		length := int(data[offset+1])<<16 | int(data[offset+2])<<8 | int(data[offset+3])
		payload := offset + 4
		if length > len(data)-payload {
			return 0, flacStatusNeedMore
		}
		if blockType == 0 {
			if length < 34 {
				return 0, flacStatusInvalid
			}
			return parseFLACStreamInfo(data[payload : payload+34])
		}
		offset = payload + length
		if last {
			return 0, flacStatusInvalid
		}
	}
}

// parseFLACStreamInfo 从 34 字节 STREAMINFO 中提取 sample rate 与 total samples。
func parseFLACStreamInfo(streamInfo []byte) (float64, int) {
	if len(streamInfo) < 18 {
		return 0, flacStatusInvalid
	}
	sampleRate := int(streamInfo[10])<<12 | int(streamInfo[11])<<4 | int(streamInfo[12])>>4
	totalSamples := uint64(streamInfo[13]&0x0F)<<32 |
		uint64(streamInfo[14])<<24 |
		uint64(streamInfo[15])<<16 |
		uint64(streamInfo[16])<<8 |
		uint64(streamInfo[17])
	if sampleRate == 0 || totalSamples == 0 {
		return 0, flacStatusInvalid
	}
	return float64(totalSamples) / float64(sampleRate), flacStatusFound
}

// flacMeter 只缓存头部元数据，找到 STREAMINFO 后不再保留任何字节。
type flacMeter struct {
	header  []byte
	seconds float64
	found   bool
	failed  bool
}

func newFLACMeter() *flacMeter {
	return &flacMeter{header: make([]byte, 0, 256)}
}

func (m *flacMeter) Write(p []byte) {
	if m.found || m.failed {
		return
	}
	for len(p) > 0 {
		space := maxHeaderBytes - len(m.header)
		if space <= 0 {
			m.failed = true
			return
		}
		take := min(len(p), space)
		if cap(m.header) < len(m.header)+take {
			newCap := cap(m.header) * 2
			if newCap < 256 {
				newCap = 256
			}
			if newCap < len(m.header)+take {
				newCap = len(m.header) + take
			}
			if newCap > maxHeaderBytes {
				newCap = maxHeaderBytes
			}
			buffer := make([]byte, len(m.header), newCap)
			copy(buffer, m.header)
			m.header = buffer
		}
		m.header = append(m.header, p[:take]...)
		p = p[take:]
		seconds, status := parseFLACHeader(m.header)
		switch status {
		case flacStatusFound:
			m.seconds = seconds
			m.found = true
			m.header = nil
			return
		case flacStatusInvalid:
			m.failed = true
			return
		}
	}
}

func (m *flacMeter) Duration() (float64, bool) {
	if m.failed || !m.found {
		return 0, false
	}
	return m.seconds, m.seconds > 0
}
