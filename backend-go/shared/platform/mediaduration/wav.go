package mediaduration

import (
	"encoding/binary"
)

// WAV（RIFF/WAVE）时长解析：fmt 的 byteRate 与 data chunk 大小。

const (
	wavStateRIFF = iota
	wavStateChunk
	wavStateFmt
	wavStateSkip
	wavStateData
	wavStateDone
)

// parseWAV 严格遍历 RIFF chunk：缺失 fmt/data、chunk 越界或 data 声明大小
// 超过实际字节都视为截断/损坏；data size 为 0/0xFFFFFFFF 视为流式占位。
func parseWAV(data []byte) (float64, bool) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return 0, false
	}

	var (
		byteRate  uint64
		haveFmt   bool
		haveData  bool
		dataBytes uint64
	)
	offset := 12
	for offset+8 <= len(data) {
		chunkID := string(data[offset : offset+4])
		size := uint64(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		payload := offset + 8

		switch chunkID {
		case "fmt ":
			if size < 16 || size > uint64(len(data)-payload) {
				return 0, false
			}
			rate, ok := wavByteRate(data[payload : payload+int(size)])
			if !ok {
				return 0, false
			}
			byteRate = rate
			haveFmt = true
			next := payload + int(size)
			if size&1 == 1 {
				next++
			}
			if next > len(data) {
				return 0, false
			}
			offset = next
		case "data":
			haveData = true
			if size == 0 || size == 0xFFFFFFFF {
				// 流式占位（0 或全 1）：剩余字节即音频数据。
				dataBytes = uint64(len(data) - payload)
				offset = len(data)
				continue
			}
			if size > uint64(len(data)-payload) {
				return 0, false
			}
			dataBytes = size
			next := payload + int(size)
			if size&1 == 1 && next < len(data) {
				next++
			}
			offset = next
		default:
			if size == 0xFFFFFFFF || size > uint64(len(data)-payload) {
				return 0, false
			}
			next := payload + int(size)
			if size&1 == 1 {
				next++
			}
			if next > len(data) {
				return 0, false
			}
			offset = next
		}
	}

	if !haveFmt || !haveData || byteRate == 0 {
		return 0, false
	}
	seconds := float64(dataBytes) / float64(byteRate)
	return seconds, seconds > 0
}

// wavByteRate 从 fmt chunk 读取 byteRate；byteRate 为 0 时用
// sampleRate×channels×bits/8 推导。
func wavByteRate(fmtChunk []byte) (uint64, bool) {
	if len(fmtChunk) < 16 {
		return 0, false
	}
	channels := uint64(binary.LittleEndian.Uint16(fmtChunk[2:4]))
	sampleRate := uint64(binary.LittleEndian.Uint32(fmtChunk[4:8]))
	byteRate := uint64(binary.LittleEndian.Uint32(fmtChunk[8:12]))
	bits := uint64(binary.LittleEndian.Uint16(fmtChunk[14:16]))
	if byteRate == 0 {
		if sampleRate == 0 || channels == 0 || bits == 0 {
			return 0, false
		}
		byteRate = sampleRate * channels * bits / 8
	}
	return byteRate, byteRate > 0
}

// wavMeter 是流式 WAV 解析状态机：只保留 RIFF/chunk/fmt 头，data 只计数。
type wavMeter struct {
	state     int
	pending   []byte
	need      int
	byteRate  uint64
	haveFmt   bool
	haveData  bool
	dataSize  uint64
	dataLimit bool
	dataBytes uint64
	skip      int64
	failed    bool
}

func newWAVMeter() *wavMeter {
	return &wavMeter{state: wavStateRIFF}
}

func (m *wavMeter) Write(p []byte) {
	for len(p) > 0 && !m.failed {
		switch m.state {
		case wavStateRIFF:
			take := min(len(p), 12-len(m.pending))
			m.pending = append(m.pending, p[:take]...)
			p = p[take:]
			if len(m.pending) < 12 {
				return
			}
			if string(m.pending[0:4]) != "RIFF" || string(m.pending[8:12]) != "WAVE" {
				m.failed = true
				return
			}
			m.pending = m.pending[:0]
			m.state = wavStateChunk
		case wavStateChunk:
			take := min(len(p), 8-len(m.pending))
			m.pending = append(m.pending, p[:take]...)
			p = p[take:]
			if len(m.pending) < 8 {
				return
			}
			chunkID := string(m.pending[0:4])
			size := uint64(binary.LittleEndian.Uint32(m.pending[4:8]))
			m.pending = m.pending[:0]
			switch chunkID {
			case "fmt ":
				if size < 16 || size > maxHeaderBytes {
					m.failed = true
					return
				}
				m.need = int(size)
				if cap(m.pending) < m.need {
					m.pending = make([]byte, 0, m.need)
				}
				m.state = wavStateFmt
			case "data":
				m.haveData = true
				if size == 0 || size == 0xFFFFFFFF {
					m.dataLimit = false
				} else {
					m.dataLimit = true
					m.dataSize = size
				}
				m.state = wavStateData
			default:
				if size == 0xFFFFFFFF {
					m.failed = true
					return
				}
				m.skip = int64(size) + int64(size&1)
				m.state = wavStateSkip
				if m.skip == 0 {
					m.state = wavStateChunk
				}
			}
		case wavStateFmt:
			take := min(len(p), m.need-len(m.pending))
			m.pending = append(m.pending, p[:take]...)
			p = p[take:]
			if len(m.pending) < m.need {
				return
			}
			rate, ok := wavByteRate(m.pending)
			if !ok {
				m.failed = true
				return
			}
			m.byteRate = rate
			m.haveFmt = true
			m.pending = m.pending[:0]
			m.need = 0
			m.state = wavStateChunk
		case wavStateSkip:
			n := int64(len(p))
			if n > m.skip {
				n = m.skip
			}
			p = p[n:]
			m.skip -= n
			if m.skip == 0 {
				m.state = wavStateChunk
			}
		case wavStateData:
			n := uint64(len(p))
			if m.dataLimit {
				remain := m.dataSize - m.dataBytes
				if n > remain {
					n = remain
				}
			}
			m.dataBytes += n
			p = p[n:]
			if m.dataLimit && m.dataBytes >= m.dataSize {
				// data 之后可能还有尾部 chunk；时长已确定，忽略剩余内容。
				m.state = wavStateDone
				return
			}
		case wavStateDone:
			return
		}
	}
}

func (m *wavMeter) Duration() (float64, bool) {
	if m.failed || !m.haveFmt || !m.haveData || m.byteRate == 0 {
		return 0, false
	}
	if m.state != wavStateData && m.state != wavStateDone {
		return 0, false
	}
	if m.dataLimit && m.dataBytes < m.dataSize {
		// 流提前结束，data 声明大小未写满。
		return 0, false
	}
	seconds := float64(m.dataBytes) / float64(m.byteRate)
	return seconds, seconds > 0
}
