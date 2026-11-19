package mediaduration

import (
	"bytes"
	"encoding/binary"
)

// MP4/M4A/MOV 时长解析：读取 moov/mvhd 的 timescale 与 duration。
// 完整解析按顶层 box 严格遍历，moov 可以在文件头或文件尾；
// Meter 使用首部 + 有界尾部缓冲扫描 moov/mvhd。

// mp4BoxHeader 是 MP4 box 头。
type mp4BoxHeader struct {
	typ       string
	headerLen int
	size      uint64 // 完整 box 大小（含头）；toEnd 时为 0
	toEnd     bool   // size == 0，表示延伸到文件/父容器末尾
}

func readMP4BoxHeader(b []byte) (mp4BoxHeader, bool) {
	if len(b) < 8 {
		return mp4BoxHeader{}, false
	}
	size := uint64(binary.BigEndian.Uint32(b[0:4]))
	header := mp4BoxHeader{
		typ:       string(b[4:8]),
		headerLen: 8,
		size:      size,
	}
	switch size {
	case 0:
		header.toEnd = true
	case 1:
		if len(b) < 16 {
			return mp4BoxHeader{}, false
		}
		header.size = binary.BigEndian.Uint64(b[8:16])
		header.headerLen = 16
		if header.size < 16 {
			return mp4BoxHeader{}, false
		}
	default:
		if header.size < 8 {
			return mp4BoxHeader{}, false
		}
	}
	return header, true
}

// parseMP4 严格遍历顶层 box；任何 box 越界都视为截断。
func parseMP4(data []byte) (float64, bool) {
	offset := 0
	seconds := 0.0
	found := false
	for offset < len(data) {
		if len(data)-offset < 8 {
			return 0, false
		}
		header, ok := readMP4BoxHeader(data[offset:])
		if !ok {
			return 0, false
		}
		size := header.size
		if header.toEnd {
			size = uint64(len(data) - offset)
		}
		if size < uint64(header.headerLen) || size > uint64(len(data)-offset) {
			return 0, false
		}
		payload := data[offset+header.headerLen : offset+int(size)]
		if header.typ == "moov" {
			parsed, ok := parseMP4MoovStrict(payload)
			if !ok {
				return 0, false
			}
			seconds = parsed
			found = true
		}
		offset += int(size)
	}
	if !found {
		return 0, false
	}
	return seconds, seconds > 0
}

// parseMP4MoovStrict 在 moov 中查找 mvhd；子 box 越界视为截断。
func parseMP4MoovStrict(payload []byte) (float64, bool) {
	offset := 0
	for offset < len(payload) {
		if len(payload)-offset < 8 {
			return 0, false
		}
		header, ok := readMP4BoxHeader(payload[offset:])
		if !ok {
			return 0, false
		}
		size := header.size
		if header.toEnd {
			size = uint64(len(payload) - offset)
		}
		if size < uint64(header.headerLen) || size > uint64(len(payload)-offset) {
			return 0, false
		}
		if header.typ == "mvhd" {
			return parseMP4Mvhd(payload[offset+header.headerLen : offset+int(size)])
		}
		offset += int(size)
	}
	return 0, false
}

// parseMP4Mvhd 解析 version 0/1 的 mvhd。
func parseMP4Mvhd(payload []byte) (float64, bool) {
	if len(payload) < 4 {
		return 0, false
	}
	var timescale uint32
	var duration uint64
	switch payload[0] {
	case 0:
		if len(payload) < 20 {
			return 0, false
		}
		timescale = binary.BigEndian.Uint32(payload[12:16])
		duration = uint64(binary.BigEndian.Uint32(payload[16:20]))
		if duration == 0xFFFFFFFF {
			return 0, false
		}
	case 1:
		if len(payload) < 32 {
			return 0, false
		}
		timescale = binary.BigEndian.Uint32(payload[20:24])
		duration = binary.BigEndian.Uint64(payload[24:32])
		if duration == 0xFFFFFFFFFFFFFFFF {
			return 0, false
		}
	default:
		return 0, false
	}
	if timescale == 0 || duration == 0 {
		return 0, false
	}
	seconds := float64(duration) / float64(timescale)
	return seconds, seconds > 0
}

// scanMP4Moov 在有界缓冲区中扫描 "moov" box 并尽力解析 mvhd。
// 适用于 Meter 的首部/尾部缓冲：缓冲区可以从中途截断，只要能读到 mvhd 字段即可。
func scanMP4Moov(buffer []byte) (float64, bool) {
	needle := []byte("moov")
	for search := 0; search+12 <= len(buffer); {
		index := bytes.Index(buffer[search:], needle)
		if index < 0 {
			break
		}
		typePos := search + index
		search = typePos + 4
		boxStart := typePos - 4
		if boxStart < 0 {
			continue
		}
		size32 := binary.BigEndian.Uint32(buffer[boxStart:])
		var payloadStart int
		switch size32 {
		case 0:
			payloadStart = boxStart + 8
		case 1:
			if boxStart+16 > len(buffer) {
				continue
			}
			payloadStart = boxStart + 16
		default:
			if size32 < 8 {
				continue
			}
			payloadStart = boxStart + 8
		}
		if payloadStart > len(buffer) {
			continue
		}
		if seconds, ok := scanMP4MoovPayload(buffer[payloadStart:]); ok {
			return seconds, true
		}
	}
	return 0, false
}

// scanMP4MoovPayload 尽力遍历 moov 子 box；遇到截断即停止。
func scanMP4MoovPayload(payload []byte) (float64, bool) {
	offset := 0
	for offset+8 <= len(payload) {
		header, ok := readMP4BoxHeader(payload[offset:])
		if !ok {
			return 0, false
		}
		size := header.size
		if header.toEnd {
			size = uint64(len(payload) - offset)
		}
		if size < uint64(header.headerLen) {
			return 0, false
		}
		available := uint64(len(payload) - offset)
		if header.typ == "mvhd" {
			start := offset + header.headerLen
			if start > len(payload) {
				return 0, false
			}
			end := offset + int(min(size, available))
			if end > len(payload) {
				end = len(payload)
			}
			return parseMP4Mvhd(payload[start:end])
		}
		if size > available {
			return 0, false
		}
		offset += int(size)
	}
	return 0, false
}
