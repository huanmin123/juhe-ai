package mockdata

// 媒体样本字节生成器的纯单元测试（与存储无关）：WAV/MP4 字节的容器合法性。
//（消息 / 资产值对象形状测试已随 my-chat 会话面退役移除，演练场设计 §8；
// WAV/MP4 生成器由 business 域 media_jobs 与 observability blob 样本继续消费。）

import (
	"testing"
)

func TestChatCodexWAVIsPlayableContainer(t *testing.T) {
	wav := chatCodexWAV()
	if len(wav) != chatCodexWAVBytes {
		t.Fatalf("WAV 字节数 %d, want %d", len(wav), chatCodexWAVBytes)
	}
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Fatal("WAV 缺 RIFF/WAVE 标识")
	}
	if string(wav[12:16]) != "fmt " {
		t.Fatal("WAV 缺 fmt 块")
	}
	// fmt 块：PCM(1)、mono(1)、8kHz、16bit。
	if wav[20] != 1 || wav[22] != 1 {
		t.Fatalf("audio format/channels 不符: %v %v", wav[20], wav[22])
	}
	if wav[24] != 0x40 || wav[25] != 0x1f { // 8000 = 0x1F40 小端
		t.Fatalf("采样率字节不符: %x %x", wav[24], wav[25])
	}
	if string(wav[36:40]) != "data" {
		t.Fatal("WAV 缺 data 块")
	}
}

func TestChatCodexMP4IsPlayableContainer(t *testing.T) {
	mp4 := chatCodexMP4()
	if len(mp4) != len(chatCodexMP4Bytes) || len(mp4) == 0 {
		t.Fatalf("MP4 字节数 %d 异常", len(mp4))
	}
	// ftyp box 前置（faststart 形态的前 12 字节：size + 'ftyp'）。
	if string(mp4[4:8]) != "ftyp" {
		t.Fatal("MP4 缺前置 ftyp box")
	}
	// 防御性拷贝：改写返回值不污染常量。
	mp4[0] = 0xFF
	if chatCodexMP4Bytes[0] == 0xFF {
		t.Fatal("chatCodexMP4 返回了共享底层切片")
	}
}
