package mockdata

// chat 域媒体样本生成器的纯单元测试（与存储无关）：WAV/MP4 字节的容器合法性
// 与消息 / 资产值对象的形状。

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
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

func TestChatMediaMessagesShape(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	messages := chatMediaMessages(now)
	if len(messages) != 8 {
		t.Fatalf("媒体演示会话应 8 轮消息: %d", len(messages))
	}
	mediaTaskStates := map[string]bool{}
	audioBlocks := 0
	for index, message := range messages {
		if message.ID == "" || message.TurnID == "" {
			t.Fatalf("消息 %d 缺 ID/TurnID", index)
		}
		if message.Role != "user" && message.Role != "assistant" {
			t.Fatalf("消息 %d 非法 role: %s", index, message.Role)
		}
		// 块必须可 JSON 编码（落库 content_blocks_json 的形状契约）。
		for _, block := range message.Blocks {
			encoded, err := json.Marshal(block)
			if err != nil {
				t.Fatalf("消息 %d 块编码失败: %v", index, err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("消息 %d 块不是对象: %v", index, err)
			}
			if blockType, _ := decoded["type"].(string); blockType == "" {
				t.Fatalf("消息 %d 块缺 type", index)
			}
			if blockType, _ := decoded["type"].(string); blockType == "output_media_task" {
				status, _ := decoded["status"].(string)
				mediaTaskStates[status] = true
			}
			if blockType, _ := decoded["type"].(string); blockType == "output_audio" {
				audioBlocks++
			}
		}
		if message.Role == "user" && message.ClientMessageID == "" {
			t.Fatalf("user 消息 %d 缺 client_message_id（PG CHECK 契约）", index)
		}
		if message.Role == "assistant" && message.ClientMessageID != "" {
			t.Fatalf("assistant 消息 %d 不应带 client_message_id（PG CHECK 契约）", index)
		}
	}
	for _, want := range []string{"in_progress", "completed", "failed"} {
		if !mediaTaskStates[want] {
			t.Fatalf("output_media_task 缺 %s 状态: %v", want, mediaTaskStates)
		}
	}
	if audioBlocks != 1 {
		t.Fatalf("output_audio 块数量 %d != 1", audioBlocks)
	}
	// 全部消息 ID 带清理标识段，幂等删除可按前缀收敛。
	for _, message := range messages {
		if !strings.HasPrefix(message.ID, CleanupIDPrefix) {
			t.Fatalf("消息 %s 缺清理标识前缀", message.ID)
		}
	}
}

func TestChatMediaAssetsShape(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	assets := chatMediaAssets(chatMediaConversationID, "owner", now)
	if len(assets) != 2 {
		t.Fatalf("媒体资产应 2 件: %d", len(assets))
	}
	mimeTypes := map[string]string{}
	for _, asset := range assets {
		if !strings.HasPrefix(asset.ID, CleanupIDPrefix) {
			t.Fatalf("资产 %s 缺清理标识前缀", asset.ID)
		}
		if asset.Conversation != chatMediaConversationID || asset.Owner != "owner" {
			t.Fatalf("资产 %s 归属不符: %+v", asset.ID, asset)
		}
		if len(asset.Bytes) == 0 || asset.QuotaBytes != len(asset.Bytes) {
			t.Fatalf("资产 %s 字节与配额不一致: %d/%d", asset.ID, len(asset.Bytes), asset.QuotaBytes)
		}
		if !asset.NoDimensions || asset.StorageVariant != "original" {
			t.Fatalf("资产 %s 形态不符（应无宽高、original variant）", asset.ID)
		}
		mimeTypes[asset.MimeType] = asset.ID
	}
	if mimeTypes["video/mp4"] == "" || mimeTypes["audio/wav"] == "" {
		t.Fatalf("缺 MP4/WAV 资产: %v", mimeTypes)
	}
}
