package chat

// w10d 覆盖收尾：commitChatAssetsToMessage / CommitChatGeneratedAsset 等
// 资产绑定链路的真实错误臂（经 AcceptTurn 与真实资产驱动）。

import (
	"strings"
	"testing"
)

// TestW10DCommitChatAssetsViaAcceptTurn 覆盖 commitChatAssetsToMessage 成功路径与错误臂。
func TestW10DCommitChatAssetsViaAcceptTurn(t *testing.T) {
	t.Run("成功路径", func(t *testing.T) {
		fixture, _ := newFaultChatFixtureW10D(t)
		fixture.createConversation("conv_w10d_ast", routeTestOwner)
		assetID := seedAssetW10D(t, fixture, "conv_w10d_ast")
		if err := acceptInputWithAssetW10DErr(fixture, "conv_w10d_ast", assetID); err != nil {
			t.Fatalf("资产绑定轮次失败: %v", err)
		}
	})

	for _, item := range []struct {
		name   string
		substr string
	}{
		{"轮次查询失败", "SELECT turn_id FROM chat_messages"},
		{"资产查询失败", "WHERE id IN ("},
		{"用户资产更新失败", "SET turn_id = ?, message_id = ?, committed_at = COALESCE"},
		{"图片谱系查询失败", "FROM chat_image_generations"},
		{"引用写入失败", "INSERT INTO chat_asset_references"},
	} {
		item := item
		runRecoverableFaultCaseW10D(t, "CommitChatAssets/"+item.name, func(f *chatFixture, script *faultScriptW10D) {
			f.createConversation("conv_w10d_ast", routeTestOwner)
			seedAssetW10D(t, f, "conv_w10d_ast")
			script.failOnce(item.substr)
		}, func(f *chatFixture) error {
			assetID := w10dLatestAssetID(f)
			_, err := f.store.AcceptTurn(acceptInputWithAssetW10D(f, "conv_w10d_ast", "cmid-ast", assetID))
			return err
		})
	}

	t.Run("资产未就绪", func(t *testing.T) {
		fixture, _ := newFaultChatFixtureW10D(t)
		fixture.createConversation("conv_w10d_ast", routeTestOwner)
		assetID := seedAssetW10D(t, fixture, "conv_w10d_ast")
		if _, err := fixture.db.Exec(`UPDATE chat_assets SET processing_status = 'pending' WHERE id = ?`, assetID); err != nil {
			t.Fatal(err)
		}
		if err := acceptInputWithAssetW10DErr(fixture, "conv_w10d_ast", assetID); err == nil {
			t.Fatal("未就绪资产应失败")
		}
	})
	t.Run("资产已过期", func(t *testing.T) {
		fixture, _ := newFaultChatFixtureW10D(t)
		fixture.createConversation("conv_w10d_ast", routeTestOwner)
		assetID := seedAssetW10D(t, fixture, "conv_w10d_ast")
		if _, err := fixture.db.Exec(`UPDATE chat_assets SET expires_at = '2020-01-01T00:00:00.000Z' WHERE id = ?`, assetID); err != nil {
			t.Fatal(err)
		}
		if err := acceptInputWithAssetW10DErr(fixture, "conv_w10d_ast", assetID); err == nil {
			t.Fatal("过期资产应失败")
		}
	})
}

// acceptInputWithAssetW10D 构造带 input_image 的接收输入。
func acceptInputWithAssetW10D(f *chatFixture, conversationID, cmid, assetID string) AcceptTurnInput {
	input := acceptTurnInputW10D(f, conversationID, cmid)
	input.ContentBlocks = []InputContentBlock{{Type: "input_image", AssetID: &assetID}}
	return input
}

func acceptInputWithAssetW10DErr(f *chatFixture, conversationID, assetID string) error {
	_, err := f.store.AcceptTurn(acceptInputWithAssetW10D(f, conversationID, "cmid-ast-e", assetID))
	return err
}

// w10dLatestAssetID 读取当前会话最近一张资产 ID。
func w10dLatestAssetID(f *chatFixture) string {
	f.t.Helper()
	var id string
	if err := f.db.QueryRow(`SELECT id FROM chat_assets WHERE conversation_id = 'conv_w10d_ast' ORDER BY created_at DESC LIMIT 1`).Scan(&id); err != nil {
		f.t.Fatalf("读取资产失败: %v", err)
	}
	return id
}

// TestW10DCommitGeneratedAssetFaultArms 覆盖 CommitChatGeneratedAsset 错误臂。
func TestW10DCommitGeneratedAssetFaultArms(t *testing.T) {
	cases := []struct {
		name   string
		substr string
	}{
		{"助手绑定查询失败", "AND turn_id = ? AND role = 'assistant'"},
		{"用量读取失败", "FROM chat_user_asset_usage"},
		{"资产写入失败", "'ready', 'not_requested', 0, ?"},
		{"引用写入失败", "'assistant_output', ?, ?, ?)"},
		{"谱系写入失败", "INSERT INTO chat_image_generations"},
		{"用量递增失败", "chat_user_asset_usage"},
		{"提交失败", commitFaultKeyW10D},
	}
	for _, item := range cases {
		item := item
		var turnID, msgID string
		runRecoverableFaultCaseW10D(t, "CommitGeneratedAsset/"+item.name, func(f *chatFixture, script *faultScriptW10D) {
			f.createConversation("conv_w10d_gen", routeTestOwner)
			turn := f.accept(routeTestOwner, "conv_w10d_gen", "cmid-gen", "问题")
			turnID = turn.TurnID
			msgID = turn.AssistantMessage.ID
			script.failOnce(item.substr)
		}, func(f *chatFixture) error {
			_, err := f.store.CommitChatGeneratedAsset(generatedCommitInputW10D(f, "conv_w10d_gen", turnID, msgID))
			return err
		})
	}
}

// generatedCommitInputW10D 构造合法生成资产提交输入。
func generatedCommitInputW10D(f *chatFixture, conversationID, turnID, msgID string) GeneratedAssetCommitInput {
	return GeneratedAssetCommitInput{
		SystemAccountID: routeTestOwner, ConversationID: conversationID, TurnID: turnID, MessageID: msgID,
		ContentOrder: 0, MimeType: "image/webp", Width: 8, Height: 8, Bytes: 64,
		Sha256: strings.Repeat("a", 64), StorageKey: "gen", PreviewBytes: 0,
		Now: f.nowISO, RetentionDays: 30,
		Generation: GeneratedImageGenerationRecord{
			Operation: "generate", Model: "gpt-image-2", Prompt: "画",
		},
	}
}

// TestW10DLoadCompactionSourcePageValidation 覆盖 LoadCompactionSourcePage 纯校验臂。
func TestW10DLoadCompactionSourcePageValidation(t *testing.T) {
	fixture, _ := newFaultChatFixtureW10D(t)
	seedCompactionClaimW10D(t, fixture, "conv_w10d_src")
	var cid string
	if err := fixture.db.QueryRow(`SELECT context_claim_id FROM chat_conversations WHERE id='conv_w10d_src'`).Scan(&cid); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.LoadCompactionSourcePage("conv_w10d_src", routeTestOwner, cid, -1, fixture.nowISO, 8, 1024); err == nil {
		t.Fatal("负 afterSequence 应失败")
	}
	if _, err := fixture.store.LoadCompactionSourcePage("conv_w10d_src", routeTestOwner, cid, 0, fixture.nowISO, 1, 1024); err == nil {
		t.Fatal("limit<2 应失败")
	}
	if _, err := fixture.store.LoadCompactionSourcePage("conv_w10d_src", routeTestOwner, cid, 0, fixture.nowISO, 8, 0); err == nil {
		t.Fatal("maxBytes<1 应失败")
	}
	page, err := fixture.store.LoadCompactionSourcePage("conv_w10d_src", routeTestOwner, "chat_context_claim_w10d_missing", 0, fixture.nowISO, 8, 1024)
	if err != nil || page != nil {
		t.Fatalf("未知认领 = %+v/%v", page, err)
	}
}
