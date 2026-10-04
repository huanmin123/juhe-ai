package mockupstream

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestPlayableMP4PayloadStructure parses PlayableMP4Payload as a real player
// would: a complete ISO-BMFF top-level box walk (sizes must tile the file
// exactly, no truncation), ftyp first with the isom brand, a real moov box
// ahead of mdat (+faststart, browser streamable), and the documented minimum
// size. Unlike mp4Payload() this payload is decodable H.264 video (see
// payload_video_playable.go for the generation recipe).
func TestPlayableMP4PayloadStructure(t *testing.T) {
	payload := PlayableMP4Payload()

	if len(payload) < 2048 {
		t.Fatalf("playable mp4 应至少 2KB: %d 字节", len(payload))
	}
	if got := PlayableMP4Payload(); !bytes.Equal(got, payload) {
		t.Fatal("多次调用应返回相同内容")
	}
	payload[0] ^= 0xFF
	if got := PlayableMP4Payload(); got[0] == payload[0] {
		t.Fatal("应返回副本：调用方修改不得污染缓存")
	}
	payload[0] ^= 0xFF // 还原突变，后续盒遍历使用原始内容

	// 顶层盒遍历：尺寸必须精确铺满整份文件（合法盒结构的必要条件）。
	type box struct {
		name string
		at   int
		size int
	}
	var boxes []box
	offset := 0
	for offset < len(payload) {
		if offset+8 > len(payload) {
			t.Fatalf("残留 %d 字节不足以构成盒头（于 %d）", len(payload)-offset, offset)
		}
		size := int(binary.BigEndian.Uint32(payload[offset : offset+4]))
		name := string(payload[offset+4 : offset+8])
		if size < 8 || offset+size > len(payload) {
			t.Fatalf("盒 %s 尺寸非法: %d（于 %d）", name, size, offset)
		}
		boxes = append(boxes, box{name: name, at: offset, size: size})
		offset += size
	}
	if offset != len(payload) {
		t.Fatalf("盒尺寸合计 %d != 文件长度 %d", offset, len(payload))
	}
	if len(boxes) < 3 {
		t.Fatalf("可播放 mp4 应至少含 ftyp/moov/mdat 三盒: %+v", boxes)
	}
	if boxes[0].name != "ftyp" || string(payload[8:12]) != "isom" {
		t.Fatalf("首盒应为 ftyp/isom: %+v", boxes[0])
	}
	var moov, mdat *box
	for index := range boxes {
		switch boxes[index].name {
		case "moov":
			if moov == nil {
				moov = &boxes[index]
			}
		case "mdat":
			if mdat == nil {
				mdat = &boxes[index]
			}
		}
	}
	if moov == nil {
		t.Fatal("载荷缺少 moov 盒（不可播放的结构性占位）")
	}
	if mdat == nil {
		t.Fatal("载荷缺少 mdat 盒")
	}
	if moov.at > mdat.at {
		t.Fatalf("moov(%d) 应先于 mdat(%d)：+faststart 契约", moov.at, mdat.at)
	}
	// moov 必须携带真实轨道结构（trak/tkhd），区分于仅 magic bytes 的占位。
	if !bytes.Contains(payload[moov.at:moov.at+moov.size], []byte("trak")) ||
		!bytes.Contains(payload[moov.at:moov.at+moov.size], []byte("tkhd")) {
		t.Fatal("moov 盒应含 trak/tkhd 轨道结构")
	}
	// 原占位载荷保持不变：/v1 域 magic bytes 断言依据。
	placeholder := mp4Payload()
	if string(placeholder[4:8]) != "ftyp" || string(placeholder[36:40]) != "mdat" || len(placeholder) != 72 {
		t.Fatalf("占位载荷不应被改动: %d 字节", len(placeholder))
	}
	if bytes.Equal(placeholder, payload) {
		t.Fatal("可播放载荷与占位载荷不应相同")
	}
}
