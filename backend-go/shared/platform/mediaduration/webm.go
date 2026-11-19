package mediaduration

import (
	"bytes"
	"encoding/binary"
	"math"
	"math/bits"
)

// WebM/Matroska 时长解析：优先读取 Segment Info 的 TimecodeScale 与 Duration；
// 缺失 Duration 时退化为最后一个 Cluster 的 Timecode + Block 相对时间码。

const (
	ebmlIDHeader        = 0x1A45DFA3
	ebmlIDSegment       = 0x18538067
	ebmlIDInfo          = 0x1549A966
	ebmlIDTimecodeScale = 0x2AD7B1
	ebmlIDDuration      = 0x4489
	ebmlIDCluster       = 0x1F43B675
	ebmlIDTimecode      = 0xE7
	ebmlIDSimpleBlock   = 0xA3
	ebmlIDBlockGroup    = 0xA0
	ebmlIDBlock         = 0xA1
)

const (
	// ebmlDefaultTimecodeScale 是默认的 1ms（纳秒单位）。
	ebmlDefaultTimecodeScale = 1000000
)

// readEBMLID 读取 1-4 字节 EBML 元素 ID（保留 marker 位）。
func readEBMLID(b []byte) (uint32, int, bool) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0, false
	}
	length := bits.LeadingZeros8(b[0]) + 1
	if length > 4 || len(b) < length {
		return 0, 0, false
	}
	var id uint32
	for i := 0; i < length; i++ {
		id = id<<8 | uint32(b[i])
	}
	return id, length, true
}

// readEBMLSize 读取 1-8 字节 EBML 变长 size；unknown 表示全 1（未知大小）。
func readEBMLSize(b []byte) (size uint64, n int, unknown bool, ok bool) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0, false, false
	}
	length := bits.LeadingZeros8(b[0]) + 1
	if length > 8 || len(b) < length {
		return 0, 0, false, false
	}
	mask := byte(0xFF >> length)
	value := uint64(b[0] & mask)
	allOnes := b[0]&mask == mask
	for i := 1; i < length; i++ {
		value = value<<8 | uint64(b[i])
		if b[i] != 0xFF {
			allOnes = false
		}
	}
	return value, length, allOnes, true
}

// readEBMLElement 读取元素 ID、size 与头部长度。
func readEBMLElement(b []byte) (id uint32, size uint64, unknown bool, headerLen int, ok bool) {
	id, idLen, ok := readEBMLID(b)
	if !ok {
		return 0, 0, false, 0, false
	}
	size, sizeLen, unknown, ok := readEBMLSize(b[idLen:])
	if !ok {
		return 0, 0, false, 0, false
	}
	return id, size, unknown, idLen + sizeLen, true
}

func readEBMLUint(b []byte) (uint64, bool) {
	if len(b) == 0 || len(b) > 8 {
		return 0, false
	}
	var value uint64
	for _, v := range b {
		value = value<<8 | uint64(v)
	}
	return value, true
}

func readEBMLFloat(b []byte) (float64, bool) {
	switch len(b) {
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b))), true
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(b)), true
	default:
		return 0, false
	}
}

// parseWebM 严格遍历 EBML 顶层，Segment size 已知时要求完整落在数据内。
func parseWebM(data []byte) (float64, bool) {
	if len(data) < 4 || binary.BigEndian.Uint32(data[0:4]) != ebmlIDHeader {
		return 0, false
	}
	offset := 0
	for offset < len(data) {
		id, size, unknown, headerLen, ok := readEBMLElement(data[offset:])
		if !ok {
			return 0, false
		}
		payload := offset + headerLen
		if unknown {
			if id != ebmlIDSegment {
				return 0, false
			}
			return parseWebMSegment(data[payload:])
		}
		if size > uint64(len(data)-payload) {
			return 0, false
		}
		if id == ebmlIDSegment {
			return parseWebMSegment(data[payload : payload+int(size)])
		}
		offset = payload + int(size)
	}
	return 0, false
}

// parseWebMSegment 遍历 Segment 子元素，记录 Info 与最后一个 Cluster。
func parseWebMSegment(segment []byte) (float64, bool) {
	offset := 0
	scale := uint64(ebmlDefaultTimecodeScale)
	haveScale := false
	duration := 0.0
	haveDuration := false
	lastClusterTimecode := uint64(0)
	lastBlockRel := int64(0)
	haveCluster := false

	for offset < len(segment) {
		id, size, unknown, headerLen, ok := readEBMLElement(segment[offset:])
		if !ok {
			return 0, false
		}
		payload := offset + headerLen
		if unknown {
			if id == ebmlIDCluster {
				tc, rel, hasTimecode, consumed := scanUnknownCluster(segment[payload:])
				if hasTimecode {
					lastClusterTimecode = tc
					lastBlockRel = rel
					haveCluster = true
				}
				if consumed <= 0 {
					// 未知 Cluster 的第一个子元素已是 Segment 级元素：Cluster 为空。
					offset = payload
				} else {
					offset = payload + consumed
				}
				continue
			}
			// 其他未知大小元素无法安全跨越，停止遍历；已收集的信息仍然有效。
			offset = len(segment)
			continue
		}
		if size > uint64(len(segment)-payload) {
			return 0, false
		}
		element := segment[payload : payload+int(size)]
		switch id {
		case ebmlIDInfo:
			parsedScale, parsedDuration, hasDuration, ok := parseWebMInfoStrict(element)
			if !ok {
				return 0, false
			}
			if parsedScale > 0 {
				scale = parsedScale
				haveScale = true
			}
			if hasDuration {
				duration = parsedDuration
				haveDuration = true
			}
		case ebmlIDCluster:
			tc, rel, ok := parseWebMClusterStrict(element)
			if ok {
				lastClusterTimecode = tc
				lastBlockRel = rel
				haveCluster = true
			}
		}
		offset = payload + int(size)
	}

	if haveDuration && duration > 0 {
		if !haveScale {
			scale = ebmlDefaultTimecodeScale
		}
		seconds := duration * float64(scale) / 1e9
		return seconds, seconds > 0
	}
	if haveCluster {
		if !haveScale {
			scale = ebmlDefaultTimecodeScale
		}
		timecode := float64(lastClusterTimecode) + float64(lastBlockRel)
		if timecode <= 0 {
			return 0, false
		}
		seconds := timecode * float64(scale) / 1e9
		return seconds, seconds > 0
	}
	return 0, false
}

// parseWebMInfoStrict 解析 Info 子元素中的 TimecodeScale 与 Duration。
func parseWebMInfoStrict(info []byte) (scale uint64, duration float64, hasDuration bool, ok bool) {
	offset := 0
	for offset < len(info) {
		id, size, unknown, headerLen, parsed := readEBMLElement(info[offset:])
		if !parsed || unknown {
			return 0, 0, false, false
		}
		payload := offset + headerLen
		if size > uint64(len(info)-payload) {
			return 0, 0, false, false
		}
		element := info[payload : payload+int(size)]
		switch id {
		case ebmlIDTimecodeScale:
			if value, parsed := readEBMLUint(element); parsed && value > 0 {
				scale = value
			}
		case ebmlIDDuration:
			if value, parsed := readEBMLFloat(element); parsed {
				duration = value
				hasDuration = true
			}
		}
		offset = payload + int(size)
	}
	return scale, duration, hasDuration, true
}

// parseWebMClusterStrict 解析 Cluster 的 Timecode 与 Block 相对时间码。
func parseWebMClusterStrict(cluster []byte) (uint64, int64, bool) {
	offset := 0
	timecode := uint64(0)
	haveTimecode := false
	relative := int64(0)
	haveRelative := false
	for offset < len(cluster) {
		id, size, unknown, headerLen, ok := readEBMLElement(cluster[offset:])
		if !ok {
			return 0, 0, false
		}
		payload := offset + headerLen
		if unknown || size > uint64(len(cluster)-payload) {
			return 0, 0, false
		}
		element := cluster[payload : payload+int(size)]
		switch id {
		case ebmlIDTimecode:
			if value, parsed := readEBMLUint(element); parsed {
				timecode = value
				haveTimecode = true
			}
		case ebmlIDSimpleBlock:
			if value, parsed := parseWebMBlockTime(element); parsed && (!haveRelative || value > relative) {
				relative = value
				haveRelative = true
			}
		case ebmlIDBlockGroup:
			if value, parsed := parseWebMBlockGroupTime(element); parsed && (!haveRelative || value > relative) {
				relative = value
				haveRelative = true
			}
		}
		offset = payload + int(size)
	}
	return timecode, relative, haveTimecode
}

// parseWebMBlockTime 读取 SimpleBlock/Block 开头的 track number 与 int16 相对时间码。
func parseWebMBlockTime(block []byte) (int64, bool) {
	_, trackLen, unknown, ok := readEBMLSize(block)
	if !ok || unknown || len(block) < trackLen+2 {
		return 0, false
	}
	return int64(int16(binary.BigEndian.Uint16(block[trackLen : trackLen+2]))), true
}

func parseWebMBlockGroupTime(group []byte) (int64, bool) {
	offset := 0
	for offset < len(group) {
		id, size, unknown, headerLen, ok := readEBMLElement(group[offset:])
		if !ok || unknown {
			return 0, false
		}
		payload := offset + headerLen
		if size > uint64(len(group)-payload) {
			return 0, false
		}
		if id == ebmlIDBlock {
			return parseWebMBlockTime(group[payload : payload+int(size)])
		}
		offset = payload + int(size)
	}
	return 0, false
}

// segmentLevelIDs 用于识别未知大小 Cluster 的结束位置。
var segmentLevelIDs = map[uint32]bool{
	ebmlIDCluster: true,
	ebmlIDInfo:    true,
	0x1654AE6B:    true, // Tracks
	0x1C53BB6B:    true, // Cues
	0x114D9B74:    true, // SeekHead
	0x1043A770:    true, // Chapters
	0x1254C367:    true, // Tags
	0x1941A469:    true, // Attachments
}

// scanUnknownCluster 尽力解析未知大小 Cluster，返回已消费字节数。
func scanUnknownCluster(data []byte) (timecode uint64, relative int64, hasTimecode bool, consumed int) {
	offset := 0
	haveRelative := false
	for offset < len(data) {
		id, size, unknown, headerLen, ok := readEBMLElement(data[offset:])
		if !ok {
			return timecode, relative, hasTimecode, len(data)
		}
		if segmentLevelIDs[id] {
			return timecode, relative, hasTimecode, offset
		}
		payload := offset + headerLen
		if unknown || size > uint64(len(data)-payload) {
			return timecode, relative, hasTimecode, offset
		}
		element := data[payload : payload+int(size)]
		switch id {
		case ebmlIDTimecode:
			if value, parsed := readEBMLUint(element); parsed {
				timecode = value
				hasTimecode = true
			}
		case ebmlIDSimpleBlock:
			if value, parsed := parseWebMBlockTime(element); parsed && (!haveRelative || value > relative) {
				relative = value
				haveRelative = true
			}
		case ebmlIDBlockGroup:
			if value, parsed := parseWebMBlockGroupTime(element); parsed && (!haveRelative || value > relative) {
				relative = value
				haveRelative = true
			}
		}
		offset = payload + int(size)
	}
	return timecode, relative, hasTimecode, offset
}

// scanWebMInfo 在有界缓冲区中查找 Info 元素并读取 TimecodeScale/Duration。
func scanWebMInfo(buffer []byte) (scale uint64, duration float64, hasDuration bool, found bool) {
	needle := []byte{0x15, 0x49, 0xA9, 0x66}
	scale = ebmlDefaultTimecodeScale
	for search := 0; search+5 <= len(buffer); {
		index := bytes.Index(buffer[search:], needle)
		if index < 0 {
			break
		}
		position := search + index
		search = position + 4
		size, sizeLen, unknown, ok := readEBMLSize(buffer[position+4:])
		if !ok || unknown {
			continue
		}
		payloadStart := position + 4 + sizeLen
		if payloadStart > len(buffer) {
			continue
		}
		end := len(buffer)
		if size <= uint64(len(buffer)-payloadStart) {
			end = payloadStart + int(size)
		}
		parsedScale, parsedDuration, parsedHasDuration, parsed := parseWebMInfoStrict(buffer[payloadStart:end])
		if !parsed {
			continue
		}
		found = true
		if parsedScale > 0 {
			scale = parsedScale
		}
		if parsedHasDuration && parsedDuration > 0 {
			return scale, parsedDuration, true, true
		}
	}
	return scale, 0, false, found
}

// scanWebMClusterTime 在缓冲区中找最后一个可解析的 Cluster 时间码。
func scanWebMClusterTime(buffer []byte) (uint64, int64, bool) {
	needle := []byte{0x1F, 0x43, 0xB6, 0x75}
	timecode := uint64(0)
	relative := int64(0)
	found := false
	for search := 0; search+5 <= len(buffer); {
		index := bytes.Index(buffer[search:], needle)
		if index < 0 {
			break
		}
		position := search + index
		search = position + 4
		size, sizeLen, unknown, ok := readEBMLSize(buffer[position+4:])
		if !ok {
			continue
		}
		payloadStart := position + 4 + sizeLen
		if payloadStart > len(buffer) {
			continue
		}
		end := len(buffer)
		if !unknown && size <= uint64(len(buffer)-payloadStart) {
			end = payloadStart + int(size)
		}
		tc, rel, hasTimecode := scanWebMClusterChildren(buffer[payloadStart:end])
		if hasTimecode {
			timecode = tc
			relative = rel
			found = true
		}
	}
	return timecode, relative, found
}

// scanWebMClusterChildren 尽力读取 Cluster 子元素，遇到截断即停止。
func scanWebMClusterChildren(cluster []byte) (uint64, int64, bool) {
	offset := 0
	timecode := uint64(0)
	haveTimecode := false
	relative := int64(0)
	haveRelative := false
	for offset+2 <= len(cluster) {
		id, size, unknown, headerLen, ok := readEBMLElement(cluster[offset:])
		if !ok || unknown {
			break
		}
		payload := offset + headerLen
		if payload > len(cluster) {
			break
		}
		end := len(cluster)
		if size <= uint64(len(cluster)-payload) {
			end = payload + int(size)
		}
		element := cluster[payload:end]
		switch id {
		case ebmlIDTimecode:
			if value, parsed := readEBMLUint(element); parsed {
				timecode = value
				haveTimecode = true
			}
		case ebmlIDSimpleBlock:
			if value, parsed := parseWebMBlockTime(element); parsed && (!haveRelative || value > relative) {
				relative = value
				haveRelative = true
			}
		case ebmlIDBlockGroup:
			if value, parsed := parseWebMBlockGroupTime(element); parsed && (!haveRelative || value > relative) {
				relative = value
				haveRelative = true
			}
		}
		if size > uint64(len(cluster)-payload) {
			break
		}
		offset = payload + int(size)
	}
	return timecode, relative, haveTimecode
}
