package mockdata

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestChatCodexWAVHeaderValid 按 RIFF 规范逐字段校验造数 WAV 头：audio format、
// 声道数、采样率、byte rate、block align、位深都是定宽小端字段，任何字段用
// 4 字节追加都会让头部错位、浏览器 <audio> 拒绝解码（M7d 截图验收实测）。
func TestChatCodexWAVHeaderValid(t *testing.T) {
	wav := chatCodexWAV()
	if len(wav) != chatCodexWAVBytes {
		t.Fatalf("WAV 总长 = %d，期望与 chatCodexWAVBytes = %d 一致", len(wav), chatCodexWAVBytes)
	}
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Fatalf("RIFF/WAVE 魔数错误: %q %q", wav[0:4], wav[8:12])
	}
	if got := binary.LittleEndian.Uint32(wav[4:8]); got != uint32(len(wav)-8) {
		t.Fatalf("RIFF size = %d，期望 %d", got, len(wav)-8)
	}
	if string(wav[12:16]) != "fmt " || binary.LittleEndian.Uint32(wav[16:20]) != 16 {
		t.Fatalf("fmt 块标识/大小错误: %q %d", wav[12:16], binary.LittleEndian.Uint32(wav[16:20]))
	}
	if got := binary.LittleEndian.Uint16(wav[20:22]); got != 1 {
		t.Fatalf("audio format = %d，期望 1（PCM）", got)
	}
	if got := binary.LittleEndian.Uint16(wav[22:24]); got != 1 {
		t.Fatalf("声道数 = %d，期望 1（mono）", got)
	}
	if got := binary.LittleEndian.Uint32(wav[24:28]); got != 8000 {
		t.Fatalf("采样率 = %d，期望 8000", got)
	}
	if got := binary.LittleEndian.Uint32(wav[28:32]); got != 16000 {
		t.Fatalf("byte rate = %d，期望 16000", got)
	}
	if got := binary.LittleEndian.Uint16(wav[32:34]); got != 2 {
		t.Fatalf("block align = %d，期望 2", got)
	}
	if got := binary.LittleEndian.Uint16(wav[34:36]); got != 16 {
		t.Fatalf("位深 = %d，期望 16", got)
	}
	if string(wav[36:40]) != "data" {
		t.Fatalf("data 块标识错误: %q", wav[36:40])
	}
	if got := binary.LittleEndian.Uint32(wav[40:44]); got != uint32(len(wav)-44) {
		t.Fatalf("data size = %d，期望 %d", got, len(wav)-44)
	}
	for i, b := range wav[44:] {
		if b != 0 {
			t.Fatalf("采样区第 %d 字节非静音: %d", i, b)
		}
	}
}

// TestChatCodexMP4Structure 钉住 MP4 样本的结构事实：总长 2024、ftyp 起始、
// moov 前置于 mdat（faststart，浏览器无需下载全文件即可起播）。常量若被误
// 编辑（丢行/丢字节），靠 magic 或自洽的长度推导都发现不了，只有结构断言能拦。
func TestChatCodexMP4Structure(t *testing.T) {
	mp4 := chatCodexMP4()
	if len(mp4) != 2024 {
		t.Fatalf("MP4 总长 = %d，期望 2024", len(mp4))
	}
	if !bytes.HasPrefix(mp4[4:], []byte("ftyp")) {
		t.Fatalf("文件头不是 ftyp box: %q", mp4[4:8])
	}
	moovAt := bytes.Index(mp4, []byte("moov"))
	mdatAt := bytes.Index(mp4, []byte("mdat"))
	if moovAt < 0 || mdatAt < 0 {
		t.Fatalf("缺少 moov(%d)/mdat(%d) box", moovAt, mdatAt)
	}
	if moovAt > mdatAt {
		t.Fatalf("moov(%d) 必须前置于 mdat(%d)：faststart 失效则浏览器无法流式起播", moovAt, mdatAt)
	}
}
