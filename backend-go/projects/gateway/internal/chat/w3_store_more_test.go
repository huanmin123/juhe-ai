package chat

import (
	"strings"
	"testing"
)

// 会话与资产存储层契约：列表分页、部分更新、清空/删除冲突、资产生命周期
// 状态机与配额守卫，以及连接丢失（关闭数据库）后的错误传播契约。

// TestConversationStoreLifecycleW3 覆盖会话存储的更新/清空/删除分支。
func TestConversationStoreLifecycleW3(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("conv_lc", "owner")

	t.Run("部分更新", func(t *testing.T) {
		title := "新名字"
		updated, err := f.store.UpdateConversation(UpdateConversationInput{
			ConversationID: "conv_lc", SystemAccountID: "owner", Title: &title, IsPinned: boolPtr(true), Now: f.nowISO,
		})
		if err != nil || updated.Title != "新名字" || !updated.IsPinned {
			t.Fatalf("更新失败: %+v err=%v", updated, err)
		}
		model := "bogus-model"
		if _, err := f.store.UpdateConversation(UpdateConversationInput{
			ConversationID: "conv_lc", SystemAccountID: "owner", DefaultImageModel: &model, Now: f.nowISO,
		}); err == nil {
			t.Fatalf("非法图片模型应报错")
		}
		good := string(ImageModelGPTImage2)
		if _, err := f.store.UpdateConversation(UpdateConversationInput{
			ConversationID: "conv_lc", SystemAccountID: "owner", DefaultImageModel: &good, Now: f.nowISO,
		}); err != nil {
			t.Fatalf("合法图片模型应通过: %v", err)
		}
		if _, err := f.store.UpdateConversation(UpdateConversationInput{
			ConversationID: "conv_lc", SystemAccountID: "owner", Now: "bad",
		}); err == nil {
			t.Fatalf("非法 now 应报错")
		}
		if missing, err := f.store.UpdateConversation(UpdateConversationInput{
			ConversationID: "none", SystemAccountID: "owner", Title: &title, Now: f.nowISO,
		}); err != nil || missing != nil {
			t.Fatalf("缺失会话应返回 nil: %+v err=%v", missing, err)
		}
	})
	t.Run("列表分页与游标", func(t *testing.T) {
		f.createConversation("conv_lc_b", "owner")
		first, err := f.store.ListConversations(ListConversationsInput{SystemAccountID: "owner", Limit: 1})
		if err != nil || len(first) != 1 {
			t.Fatalf("首页失败: %v", err)
		}
		pinned := first[0].IsPinned
		cursor, err := f.store.ListConversations(ListConversationsInput{
			SystemAccountID: "owner", Limit: 10,
			BeforeIsPinned: &pinned, BeforeLastMessageAt: strPtrT(first[0].LastMessageAt), BeforeID: strPtrT(first[0].ID),
		})
		if err != nil {
			t.Fatalf("游标页失败: %v", err)
		}
		for _, conversation := range cursor {
			if conversation.ID == first[0].ID {
				t.Fatalf("游标页不应包含首页会话")
			}
		}
		if _, err := f.store.ListConversations(ListConversationsInput{
			SystemAccountID: "owner", BeforeLastMessageAt: strPtrT("bad"),
		}); err == nil {
			t.Fatalf("非法游标应报错")
		}
		if foreign, err := f.store.ListConversations(ListConversationsInput{SystemAccountID: "other", Limit: 10}); err != nil || len(foreign) != 0 {
			t.Fatalf("跨用户列表应为空: %v err=%v", foreign, err)
		}
	})
	t.Run("清空与冲突", func(t *testing.T) {
		f.seedTurns("owner", "conv_lc_b", 2)
		cleared, err := f.store.ClearConversation(ClearConversationInput{ConversationID: "conv_lc_b", SystemAccountID: "owner", Now: f.nowISO})
		if err != nil || cleared.UserTurnCount != 0 {
			t.Fatalf("清空失败: %+v err=%v", cleared, err)
		}
		if missing, err := f.store.ClearConversation(ClearConversationInput{ConversationID: "none", SystemAccountID: "owner", Now: f.nowISO}); err != nil || missing != nil {
			t.Fatalf("缺失会话清空应返回 nil: %+v err=%v", missing, err)
		}
		if _, err := f.store.ClearConversation(ClearConversationInput{ConversationID: "conv_lc_b", SystemAccountID: "owner", Now: "bad"}); err == nil {
			t.Fatalf("非法 now 应报错")
		}
	})
	t.Run("活动轮清空冲突", func(t *testing.T) {
		accepted := f.accept("owner", "conv_lc", "cmid-active", "问题")
		if _, err := f.store.ClearConversation(ClearConversationInput{ConversationID: "conv_lc", SystemAccountID: "owner", Now: f.nowISO}); err == nil {
			t.Fatalf("活动轮清空应冲突")
		}
		f.complete("owner", "conv_lc", accepted.TurnID, "回答")
	})
	t.Run("删除", func(t *testing.T) {
		deleted, err := f.store.DeleteConversation("conv_lc_b", "owner")
		if err != nil || !deleted {
			t.Fatalf("删除失败: %v err=%v", deleted, err)
		}
		if again, err := f.store.DeleteConversation("conv_lc_b", "owner"); err != nil || again {
			t.Fatalf("重复删除应返回 false: %v err=%v", again, err)
		}
	})
}

// TestTitleFromContentW3 覆盖标题派生契约。
func TestTitleFromContentW3(t *testing.T) {
	if got := TitleFromContent("  多   行\n标题 \u00a0 "); got != "多 行 标题" {
		t.Fatalf("标题折叠不正确: %q", got)
	}
	if got := TitleFromContent(strings.Repeat("长", 80)); len([]rune(got)) != 60 {
		t.Fatalf("标题应截断到 60: %d", len([]rune(got)))
	}
	if got := TitleFromContent("\n \t "); got != "新对话" {
		t.Fatalf("空白标题应回退: %q", got)
	}
}

// TestAssetStoreLifecycleW3 覆盖资产生命周期存储契约。
func TestAssetStoreLifecycleW3(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("conv_asset", "owner")
	created, err := f.store.CreateChatAsset(CreateChatAssetInput{
		SystemAccountID: "owner", ConversationID: "conv_asset", SourceKind: "user_upload",
		OriginalFilename: "cat.png", OriginalMimeType: "image/png",
		OriginalBytes: 10, OriginalSha256: strings.Repeat("a", 64),
		QuotaBytes: 1024, Now: f.nowISO, RetentionDays: 30,
	})
	if err != nil || created.ProcessingStatus != "pending" {
		t.Fatalf("创建失败: %+v err=%v", created, err)
	}
	if _, err := f.store.CreateChatAsset(CreateChatAssetInput{
		SystemAccountID: "owner", ConversationID: "conv_asset", SourceKind: "user_upload",
		OriginalBytes: 10, QuotaBytes: 0, Now: "bad", RetentionDays: 30,
	}); err == nil {
		t.Fatalf("非法 now 应报错")
	}
	// 打满 5 个未提交资产后触发数量上限。
	for i := 0; i < 4; i++ {
		if _, err := f.store.CreateChatAsset(CreateChatAssetInput{
			SystemAccountID: "owner", ConversationID: "conv_asset", SourceKind: "user_upload",
			OriginalBytes: 10, OriginalSha256: strings.Repeat(string(rune('a'+i)), 64),
			QuotaBytes: 1024, Now: f.nowISO, RetentionDays: 30,
		}); err != nil {
			t.Fatalf("批量创建失败: %v", err)
		}
	}
	if _, err := f.store.CreateChatAsset(CreateChatAssetInput{
		SystemAccountID: "owner", ConversationID: "conv_asset", SourceKind: "user_upload",
		OriginalBytes: 10, QuotaBytes: 1024, Now: f.nowISO, RetentionDays: 30,
	}); err == nil {
		t.Fatalf("第 6 个资产应触发数量上限")
	} else if _, ok := err.(*AssetCountExceededError); !ok {
		t.Fatalf("应返回数量上限错误: %v", err)
	}
	slots, slotErr := f.store.AssertChatAssetUploadSlotAvailable("owner", "conv_asset", f.nowISO)
	if slotErr == nil || slots != 0 {
		t.Fatalf("满槽应报数量上限错误: slots=%d err=%v", slots, slotErr)
	}
	freeSlots, freeErr := f.store.AssertChatAssetUploadSlotAvailable("owner", "conv_gen_free", f.nowISO)
	if freeErr != nil || freeSlots != maxChatAssetsPerMessage {
		t.Fatalf("空会话应有全部槽位: %d err=%v", freeSlots, freeErr)
	}
	// 完成处理。
	completed, err := f.store.CompleteChatAssetProcessing(CompleteAssetProcessingInput{
		AssetID: created.ID, SystemAccountID: "owner", ConversationID: "conv_asset",
		ProcessedMimeType: "image/webp", ProcessedWidth: 1, ProcessedHeight: 1,
		ProcessedBytes: 10, ProcessedSha256: strings.Repeat("b", 64),
		StorageKey:     "aa/bb/key", Now: f.nowISO,
	})
	if err != nil || completed.ProcessingStatus != "ready" {
		t.Fatalf("完成处理失败: %+v err=%v", completed, err)
	}
	metadata, err := AssetAPIMetadataOf(completed)
	if err != nil || metadata.MimeType != "image/webp" {
		t.Fatalf("元数据转换失败: %+v err=%v", metadata, err)
	}
	if _, err := AssetAPIMetadataOf(&Asset{ProcessingStatus: "pending"}); err == nil {
		t.Fatalf("未处理资产应报错")
	}
	if _, err := f.store.CompleteChatAssetProcessing(CompleteAssetProcessingInput{
		AssetID: "missing", SystemAccountID: "owner", ConversationID: "conv_asset", Now: f.nowISO,
	}); err == nil {
		t.Fatalf("缺失资产完成处理应报错")
	}
	ready, err := f.store.ListReadyAssetsByID([]string{created.ID}, "owner", "conv_asset", f.nowISO)
	if err != nil || len(ready) != 1 {
		t.Fatalf("就绪列表失败: %v err=%v", ready, err)
	}
	byID, err := f.store.GetAsset(created.ID, "owner", "conv_asset", f.nowISO)
	if err != nil || byID == nil {
		t.Fatalf("读取资产失败: %v", err)
	}
	if noGuard, err := f.store.GetAssetNoExpiryGuard(created.ID, "owner", "conv_asset"); err != nil || noGuard == nil {
		t.Fatalf("无过期守卫读取失败: %v", err)
	}
	if _, err := f.store.FailChatAssetProcessing(created.ID, "owner", "conv_asset", "boom", f.nowISO); err != nil {
		t.Fatalf("标记失败不应报错: %v", err)
	}
	// 未绑定消息的资产可认领删除并完成删除闭环。
	claim, err := f.store.ClaimUncommittedAssetForDeletion(created.ID, "owner", "conv_asset", f.nowISO)
	if err != nil || claim == nil || claim.Asset == nil {
		t.Fatalf("未绑定资产应可认领: %+v err=%v", claim, err)
	}
	if done, err := f.store.CompleteAssetDeletion(created.ID, claim.ClaimID); err != nil || !done {
		t.Fatalf("完成删除失败: %v err=%v", done, err)
	}
	if gone, err := f.store.GetAsset(created.ID, "owner", "conv_asset", f.nowISO); err != nil || gone != nil {
		t.Fatalf("删除后资产应不可见: %+v err=%v", gone, err)
	}
	if again, err := f.store.ClaimUncommittedAssetForDeletion(created.ID, "owner", "conv_asset", f.nowISO); err != nil || again != nil {
		t.Fatalf("已删除资产不应再认领: %+v err=%v", again, err)
	}
	if ok, err := f.store.ReleaseAssetDeletionClaim(created.ID, "claim", "", f.nowISO, f.nowISO); err != nil || ok {
		t.Fatalf("释放不存在的认领应返回 false: %v err=%v", ok, err)
	}
	if got := normalizedErrorCode(""); got != "chat_asset_delete_failed" {
		t.Fatalf("默认错误码不正确: %s", got)
	}
}

// TestCommitGeneratedAssetW3 覆盖生成资产提交与谱系索引。
func TestCommitGeneratedAssetW3(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("conv_gen", "owner")
	accepted := f.accept("owner", "conv_gen", "cmid-1", "画一只猫")
	f.complete("owner", "conv_gen", accepted.TurnID, "已生成")
	asset, err := f.store.CommitChatGeneratedAsset(GeneratedAssetCommitInput{
		SystemAccountID: "owner", ConversationID: "conv_gen",
		TurnID: accepted.TurnID, MessageID: accepted.AssistantMessage.ID, ContentOrder: 0,
		MimeType: "image/png", Width: 8, Height: 8, Bytes: 64, Sha256: strings.Repeat("c", 64),
		StorageKey: "cc/ck/key", PreviewMimeType: "image/webp",
		PreviewWidth: 4, PreviewHeight: 4, PreviewBytes: 16, PreviewSha256: strings.Repeat("d", 64),
		PreviewStorageKey: "cc/ck/preview", Now: f.nowISO, RetentionDays: 30,
		Generation: GeneratedImageGenerationRecord{Operation: "generate", Model: "gpt-image-2", Prompt: "一只猫", Size: "auto", Quality: "auto", OutputFormat: "webp"},
	})
	if err != nil || asset.SourceKind != "assistant_generated" {
		t.Fatalf("生成资产提交失败: %+v err=%v", asset, err)
	}
	records, err := f.store.ListRecentImageGenerations("conv_gen", "owner", f.nowISO, 12)
	if err != nil || len(records) != 1 || records[0].AssetID != asset.ID {
		t.Fatalf("谱系索引失败: %+v err=%v", records, err)
	}
}

// TestClosedDatabaseErrorsW3 以关闭数据库为故障注入，验证存储层错误传播
// 而非 panic（连接丢失场景的错误分支覆盖）。
func TestClosedDatabaseErrorsW3(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("conv_closed", "owner")
	if err := f.db.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}
	assertError := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s 应在连接丢失时返回错误", name)
		}
	}
	_, err := f.store.AcceptTurn(AcceptTurnInput{ConversationID: "conv_closed", SystemAccountID: "owner", ClientMessageID: "c", Now: f.nowISO})
	assertError("AcceptTurn", err)
	_, err = f.store.CompleteChatTurn(CompleteTurnInput{ConversationID: "conv_closed", SystemAccountID: "owner", TurnID: "t", Now: f.nowISO})
	assertError("CompleteChatTurn", err)
	_, err = f.store.ListMessages(ListMessagesInput{ConversationID: "conv_closed", SystemAccountID: "owner", Now: f.nowISO, Limit: 1})
	assertError("ListMessages", err)
	_, err = f.store.GetConversation("conv_closed", "owner")
	assertError("GetConversation", err)
	_, err = f.store.ListConversations(ListConversationsInput{SystemAccountID: "owner", Limit: 1})
	assertError("ListConversations", err)
	_, err = f.store.CreateChatAsset(CreateChatAssetInput{SystemAccountID: "owner", ConversationID: "conv_closed", Now: f.nowISO})
	assertError("CreateChatAsset", err)
	_, err = f.store.GetAsset("a", "owner", "conv_closed", f.nowISO)
	assertError("GetAsset", err)
	_, err = f.store.ListReadyAssetsByID([]string{"chat_asset_" + strings.Repeat("a", 32)}, "owner", "conv_closed", f.nowISO)
	assertError("ListReadyAssetsByID", err)
	_, err = f.store.LoadModelContext("conv_closed", "owner", f.nowISO, 512, 1024)
	assertError("LoadModelContext", err)
	_, err = f.store.GetContextHead("conv_closed", "owner")
	assertError("GetContextHead", err)
	_, err = f.store.FindTurnByClientMessageID("conv_closed", "owner", "c")
	assertError("FindTurnByClientMessageID", err)
	_, err = f.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{ConversationID: "conv_closed", SystemAccountID: "owner", ExpectedTurnID: "t", Now: f.nowISO})
	assertError("CancelActiveTurnIfMatches", err)
	_, err = f.store.GetConversationSyncHead("conv_closed", "owner", f.nowISO)
	assertError("GetConversationSyncHead", err)
	err = f.store.AssertTurnReplaceable(AssertReplaceableInput{ConversationID: "conv_closed", SystemAccountID: "owner", ReplaceTurnID: "t", Now: f.nowISO})
	assertError("AssertTurnReplaceable", err)
	_, err = f.store.ListRecentImageGenerations("conv_closed", "owner", f.nowISO, 1)
	assertError("ListRecentImageGenerations", err)
	_, err = f.store.LoadCompactionSourcePage("conv_closed", "owner", "claim", 0, f.nowISO, 10, 1024)
	assertError("LoadCompactionSourcePage", err)
	_, err = f.store.RecordContextUsage(RecordContextUsageInput{ConversationID: "conv_closed", SystemAccountID: "owner", Now: f.nowISO})
	assertError("RecordContextUsage", err)
	err = f.store.releaseConversationStorageAndExpireAssets(f.db, "conv_closed", "owner", f.nowISO)
	assertError("releaseConversationStorageAndExpireAssets", err)
}
