package chat

// w10d 覆盖收尾：assets / assets_store / context 的 store 层单条语句故障注入。

import (
	"testing"
)

// seedAssetW10D 创建并完成一张资产行，返回资产 ID。
func seedAssetW10D(t *testing.T, f *chatFixture, conversationID string) string {
	t.Helper()
	created, err := f.store.CreateChatAsset(CreateChatAssetInput{
		ID: "", SystemAccountID: routeTestOwner, ConversationID: conversationID,
		SourceKind: "user_upload", OriginalBytes: 10, OriginalSha256: stringsRepeatW10D("e", 64),
		QuotaBytes: 10, Now: f.nowISO, RetentionDays: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := f.store.CompleteChatAssetProcessing(CompleteAssetProcessingInput{
		AssetID: created.ID, SystemAccountID: routeTestOwner, ConversationID: conversationID,
		ProcessedMimeType: "image/webp", ProcessedWidth: 1, ProcessedHeight: 1, ProcessedBytes: 10,
		ProcessedSha256: stringsRepeatW10D("f", 64), StorageKey: "k", Now: f.nowISO,
	})
	if err != nil || completed == nil {
		t.Fatalf("完成资产处理失败: %v %v", completed, err)
	}
	return completed.ID
}

func stringsRepeatW10D(value string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += value
	}
	return out
}

// seedCompactionClaimW10D 造出压缩认领（compacting）状态。
func seedCompactionClaimW10D(t *testing.T, f *chatFixture, conversationID string) *ContextCompactionClaim {
	t.Helper()
	f.createConversation(conversationID, routeTestOwner)
	f.seedTurns(routeTestOwner, conversationID, 2)
	head, err := f.store.GetContextHead(conversationID, routeTestOwner)
	if err != nil {
		t.Fatal(err)
	}
	if !requestCompaction(t, f, conversationID, routeTestOwner, head.ContextRevision, 2) {
		t.Fatal("请求压缩失败")
	}
	claim, err := f.store.ClaimContextCompaction(ClaimCompactionInput{
		ConversationID: conversationID, SystemAccountID: routeTestOwner,
		ExpectedRevision: head.ContextRevision, SourceThroughSequence: 2,
		Now: f.nowISO, StaleClaimBefore: f.nowISO,
	})
	if err != nil || claim == nil {
		t.Fatalf("认领压缩失败: %v %v", claim, err)
	}
	return claim
}

// seedProgressAndClaimW10D 返回（claim, headRevision），状态进入 compacting 且进度推进到窗口。
func seedProgressAndClaimW10D(t *testing.T, f *chatFixture, conversationID string) (*ContextCompactionClaim, int64) {
	t.Helper()
	claim := seedCompactionClaimW10D(t, f, conversationID)
	head, _ := f.store.GetContextHead(conversationID, routeTestOwner)
	if progressed, err := f.store.RecordCompactionProgress(RecordCompactionProgressInput{
		ConversationID: conversationID, SystemAccountID: routeTestOwner, ClaimID: claim.ClaimID,
		ThroughSequence: 2, EarliestExpiresAt: "2026-03-11T08:00:00.000Z", Now: f.nowISO,
	}); err != nil || !progressed {
		t.Fatalf("推进进度失败: %v %v", progressed, err)
	}
	return claim, head.ContextRevision
}

// installInputW10D 构造一个合法、可成功安装的 checkpoint 输入。
func installInputW10D(f *chatFixture, conversationID string, claim *ContextCompactionClaim, revision int64) InstallCheckpointInput {
	return InstallCheckpointInput{
		ClaimID: claim.ClaimID, ConversationID: conversationID, SystemAccountID: routeTestOwner,
		SourceRevision: revision, SourceThroughSequence: 2,
		ExpiresAt: "2026-03-11T08:00:00.000Z", PayloadDigest: digest64("ab"),
		EstimatedInputTokens: int64Ptr(120), EffectiveContextLimitTokens: int64Ptr(1000),
		RequestBodyBytes: 2048, ModelID: "gpt-5", EndpointFamily: "responses", PromptVersion: "v1",
		Entries: []CheckpointEntryInput{
			{Kind: "verbatim", Content: jsonRaw(`{"role":"user","text":"问题1"}`), Provenance: "user", TrustLevel: "untrusted", TokenCount: int64Ptr(10)},
			{Kind: "verbatim", Content: jsonRaw(`{"role":"assistant","text":"回答1"}`), Provenance: "assistant", TrustLevel: "assistant_derived"},
		},
		Now: f.nowISO,
	}
}

// TestW10DAssetStoreFaultArms 覆盖 assets_store 方法错误臂。
func TestW10DAssetStoreFaultArms(t *testing.T) {
	t.Run("AssertChatAssetUploadSlotAvailable/计数失败", func(t *testing.T) {
		fixture, script := newFaultChatFixtureW10D(t)
		fixture.createConversation("conv_w10d_slot", routeTestOwner)
		script.failOnce("COUNT(*) AS total FROM chat_assets")
		if _, err := fixture.store.AssertChatAssetUploadSlotAvailable(routeTestOwner, "conv_w10d_slot", fixture.nowISO); err == nil {
			t.Fatalf("注入故障未生效")
		}
	})
	t.Run("AssertChatAssetUploadSlotAvailable/提交失败", func(t *testing.T) {
		fixture, script := newFaultChatFixtureW10D(t)
		fixture.createConversation("conv_w10d_slot", routeTestOwner)
		script.failOnce(commitFaultKeyW10D)
		if _, err := fixture.store.AssertChatAssetUploadSlotAvailable(routeTestOwner, "conv_w10d_slot", fixture.nowISO); err == nil {
			t.Fatalf("注入故障未生效")
		}
	})

	createCases := []struct {
		name   string
		substr string
		nth    int
	}{
		{"未提交计数失败", "COUNT(*) AS total FROM chat_assets", 1},
		{"用量读取失败", "FROM chat_user_asset_usage", 1},
		{"资产写入失败", "'pending', 'not_requested', 'active', 0, ?, ?, ?", 1},
		{"用量递增失败", "chat_user_asset_usage", 2},
		{"提交失败", commitFaultKeyW10D, 1},
		{"写入后读取失败", "FROM chat_assets", 2},
	}
	for _, item := range createCases {
		item := item
		runRecoverableFaultCaseW10D(t, "CreateChatAsset/"+item.name, func(f *chatFixture, script *faultScriptW10D) {
			f.createConversation("conv_w10d_asset", routeTestOwner)
			if item.nth == 1 {
				script.failOnce(item.substr)
			} else {
				script.failNth(item.substr, item.nth)
			}
		}, func(f *chatFixture) error {
			_, err := f.store.CreateChatAsset(CreateChatAssetInput{
				ID: "", SystemAccountID: routeTestOwner, ConversationID: "conv_w10d_asset",
				SourceKind: "user_upload", OriginalBytes: 10, OriginalSha256: stringsRepeatW10D("a", 64),
				QuotaBytes: 10, Now: f.nowISO, RetentionDays: 30,
			})
			return err
		})
	}

	// 会话不属于用户 → affected != 1 臂。
	t.Run("CreateChatAsset/会话不存在", func(t *testing.T) {
		fixture, _ := newFaultChatFixtureW10D(t)
		_, err := fixture.store.CreateChatAsset(CreateChatAssetInput{
			ID: "", SystemAccountID: routeTestOwner, ConversationID: "conv_w10d_missing",
			SourceKind: "user_upload", OriginalBytes: 10, OriginalSha256: stringsRepeatW10D("b", 64),
			QuotaBytes: 10, Now: fixture.nowISO, RetentionDays: 30,
		})
		if err == nil || err.Error() != "聊天会话不存在或不属于当前用户" {
			t.Fatalf("会话不存在 = %v", err)
		}
	})

	// GetAsset 查询失败臂。
	t.Run("GetAsset/查询失败", func(t *testing.T) {
		fixture, script := newFaultChatFixtureW10D(t)
		fixture.createConversation("conv_w10d_asset", routeTestOwner)
		script.failOnce("FROM chat_assets")
		if _, err := fixture.store.GetAsset("chat_asset_fake", routeTestOwner, "conv_w10d_asset", fixture.nowISO); err == nil {
			t.Fatalf("注入故障未生效")
		}
	})

	// CompleteChatAssetProcessing / FailChatAssetProcessing 更新失败臂。
	t.Run("CompleteChatAssetProcessing/更新失败", func(t *testing.T) {
		fixture, script := newFaultChatFixtureW10D(t)
		fixture.createConversation("conv_w10d_asset", routeTestOwner)
		assetID := seedAssetW10D(t, fixture, "conv_w10d_asset")
		script.failOnce("SET processed_mime_type = ?")
		if _, err := fixture.store.CompleteChatAssetProcessing(CompleteAssetProcessingInput{
			AssetID: assetID, SystemAccountID: routeTestOwner, ConversationID: "conv_w10d_asset",
			ProcessedMimeType: "image/webp", ProcessedWidth: 1, ProcessedHeight: 1, ProcessedBytes: 11,
			ProcessedSha256: stringsRepeatW10D("c", 64), StorageKey: "k2", Now: fixture.nowISO,
		}); err == nil {
			t.Fatalf("注入故障未生效")
		}
	})
	t.Run("FailChatAssetProcessing/更新失败", func(t *testing.T) {
		fixture, script := newFaultChatFixtureW10D(t)
		fixture.createConversation("conv_w10d_asset", routeTestOwner)
		assetID := seedAssetW10D(t, fixture, "conv_w10d_asset")
		script.failOnce("SET processing_status = 'failed', processing_error_code")
		if _, err := fixture.store.FailChatAssetProcessing(assetID, routeTestOwner, "conv_w10d_asset", "code", fixture.nowISO); err == nil {
			t.Fatalf("注入故障未生效")
		}
	})

	// ListRecentImageGenerations 查询失败臂。
	t.Run("ListRecentImageGenerations/查询失败", func(t *testing.T) {
		fixture, script := newFaultChatFixtureW10D(t)
		fixture.createConversation("conv_w10d_asset", routeTestOwner)
		script.failOnce("FROM chat_image_generations")
		if _, err := fixture.store.ListRecentImageGenerations("conv_w10d_asset", routeTestOwner, fixture.nowISO, 5); err == nil {
			t.Fatalf("注入故障未生效")
		}
	})

	// LoadCompactionSourcePage 认领/查询失败臂。
	t.Run("LoadCompactionSourcePage/认领读取失败", func(t *testing.T) {
		fixture, script := newFaultChatFixtureW10D(t)
		claim := seedCompactionClaimW10D(t, fixture, "conv_w10d_src")
		script.failOnce("FROM chat_conversations")
		if _, err := fixture.store.LoadCompactionSourcePage("conv_w10d_src", routeTestOwner, claim.ClaimID, 0, fixture.nowISO, 8, 1024); err == nil {
			t.Fatalf("注入故障未生效")
		}
	})
	t.Run("LoadCompactionSourcePage/来源查询失败", func(t *testing.T) {
		fixture, script := newFaultChatFixtureW10D(t)
		claim := seedCompactionClaimW10D(t, fixture, "conv_w10d_src")
		script.failOnce("FROM chat_messages AS source")
		if _, err := fixture.store.LoadCompactionSourcePage("conv_w10d_src", routeTestOwner, claim.ClaimID, 0, fixture.nowISO, 8, 1024); err == nil {
			t.Fatalf("注入故障未生效")
		}
	})
}

// TestW10DAssetFaultArms 覆盖 assets.go 方法错误臂。
func TestW10DAssetFaultArms(t *testing.T) {
	t.Run("ClaimUncommittedAssetForDeletion/更新失败", func(t *testing.T) {
		fixture, script := newFaultChatFixtureW10D(t)
		fixture.createConversation("conv_w10d_asset", routeTestOwner)
		assetID := seedAssetW10D(t, fixture, "conv_w10d_asset")
		script.failOnce("SET cleanup_status = 'claimed'")
		if _, err := fixture.store.ClaimUncommittedAssetForDeletion(assetID, routeTestOwner, "conv_w10d_asset", fixture.nowISO); err == nil {
			t.Fatalf("注入故障未生效")
		}
	})

	// CompleteAssetDeletion：需要先造一张已认领的资产。
	seedClaimed := func(f *chatFixture) (string, string) {
		assetID := seedAssetW10D(t, f, "conv_w10d_asset")
		claim, err := f.store.ClaimUncommittedAssetForDeletion(assetID, routeTestOwner, "conv_w10d_asset", f.nowISO)
		if err != nil || claim == nil {
			t.Fatalf("认领删除失败: %v %v", claim, err)
		}
		return claim.ClaimID, assetID
	}
	completeCases := []struct {
		name   string
		substr string
	}{
		{"删除失败", "DELETE FROM chat_assets"},
		{"用量扣减失败", "SET asset_bytes = CASE WHEN asset_bytes >= ?"},
		{"提交失败", commitFaultKeyW10D},
	}
	for _, item := range completeCases {
		item := item
		var claimID, assetID string
		runRecoverableFaultCaseW10D(t, "CompleteAssetDeletion/"+item.name, func(f *chatFixture, script *faultScriptW10D) {
			f.createConversation("conv_w10d_asset", routeTestOwner)
			claimID, assetID = seedClaimed(f)
			if item.substr != "" {
				script.failOnce(item.substr)
			}
		}, func(f *chatFixture) error {
			_, err := f.store.CompleteAssetDeletion(assetID, claimID)
			return err
		})
	}

	t.Run("ReleaseAssetDeletionClaim/更新失败", func(t *testing.T) {
		fixture, script := newFaultChatFixtureW10D(t)
		fixture.createConversation("conv_w10d_asset", routeTestOwner)
		assetID := seedAssetW10D(t, fixture, "conv_w10d_asset")
		claim, _ := fixture.store.ClaimUncommittedAssetForDeletion(assetID, routeTestOwner, "conv_w10d_asset", fixture.nowISO)
		script.failOnce("SET cleanup_status = 'failed', cleanup_claim_id = NULL")
		if _, err := fixture.store.ReleaseAssetDeletionClaim(assetID, claim.ClaimID, "code", fixture.nowISO, fixture.nowISO); err == nil {
			t.Fatalf("注入故障未生效")
		}
	})
}

// w10dClaimedClaimID 读取当前会话最大已认领资产对应的 claim_id。
func w10dClaimedClaimID(f *chatFixture) string {
	f.t.Helper()
	var claimID string
	if err := f.db.QueryRow(`SELECT cleanup_claim_id FROM chat_assets WHERE cleanup_status = 'claimed' LIMIT 1`).Scan(&claimID); err != nil {
		f.t.Fatalf("读取认领记录失败: %v", err)
	}
	return claimID
}

// TestW10DContextFaultArms 覆盖 context.go 方法错误臂。
func TestW10DContextFaultArms(t *testing.T) {
	t.Run("RequestContextCompaction/更新失败", func(t *testing.T) {
		fixture, script := newFaultChatFixtureW10D(t)
		fixture.createConversation("conv_w10d_ctx", routeTestOwner)
		fixture.seedTurns(routeTestOwner, "conv_w10d_ctx", 2)
		script.failOnce("SET context_state = 'compact_pending'")
		head, _ := fixture.store.GetContextHead("conv_w10d_ctx", routeTestOwner)
		if _, err := fixture.store.RequestContextCompaction(RequestCompactionInput{
			ConversationID: "conv_w10d_ctx", SystemAccountID: routeTestOwner,
			ExpectedRevision: head.ContextRevision, SourceThroughSequence: 2, Now: fixture.nowISO,
		}); err == nil {
			t.Fatalf("注入故障未生效")
		}
	})

	claimCases := []struct {
		name   string
		substr string
		nth    int
	}{
		{"确定性头查询失败", "FROM chat_conversations", 1},
		{"认领更新失败", "context_state = 'compacting', context_claim_id = ?", 1},
		{"认领回读失败", "FROM chat_conversations", 2},
		{"提交失败", commitFaultKeyW10D, 1},
	}
	for _, item := range claimCases {
		item := item
		var revision int64
		runRecoverableFaultCaseW10D(t, "ClaimContextCompaction/"+item.name, func(f *chatFixture, script *faultScriptW10D) {
			f.createConversation("conv_w10d_ctx", routeTestOwner)
			f.seedTurns(routeTestOwner, "conv_w10d_ctx", 2)
			head, _ := f.store.GetContextHead("conv_w10d_ctx", routeTestOwner)
			revision = head.ContextRevision
			if !requestCompaction(t, f, "conv_w10d_ctx", routeTestOwner, revision, 2) {
				t.Fatal("请求压缩失败")
			}
			if item.nth == 1 {
				script.failOnce(item.substr)
			} else {
				script.failNth(item.substr, item.nth)
			}
		}, func(f *chatFixture) error {
			_, err := f.store.ClaimContextCompaction(ClaimCompactionInput{
				ConversationID: "conv_w10d_ctx", SystemAccountID: routeTestOwner,
				ExpectedRevision: revision, SourceThroughSequence: 2,
				Now: f.nowISO, StaleClaimBefore: f.nowISO,
			})
			return err
		})
	}

	installCases := []struct {
		name   string
		substr string
	}{
		{"确定性头查询失败", "FROM chat_conversations"},
		{"checkpoint 写入失败", "INSERT INTO chat_context_checkpoints"},
		{"entries 写入失败", "INSERT INTO chat_context_entries"},
		{"supersede 更新失败", "SET status = 'superseded'"},
		{"安装后回读失败", "FROM chat_context_checkpoints"},
		{"提交失败", commitFaultKeyW10D},
	}
	for _, item := range installCases {
		item := item
		var claim *ContextCompactionClaim
		var revision int64
		runRecoverableFaultCaseW10D(t, "InstallContextCheckpoint/"+item.name, func(f *chatFixture, script *faultScriptW10D) {
			claim, revision = seedProgressAndClaimW10D(t, f, "conv_w10d_ctx")
			script.failOnce(item.substr)
		}, func(f *chatFixture) error {
			_, err := f.store.InstallContextCheckpoint(installInputW10D(f, "conv_w10d_ctx", claim, revision))
			return err
		})
	}

	// LoadModelContext 查询失败臂。
	loadCases := []struct {
		name   string
		substr string
		seed   func(f *chatFixture)
	}{
		{"头查询失败", "FROM chat_conversations", func(f *chatFixture) {
			f.createConversation("conv_w10d_ctx", routeTestOwner)
			f.seedTurns(routeTestOwner, "conv_w10d_ctx", 2)
		}},
		{"来源轮次查询失败", "FROM chat_messages AS source", func(f *chatFixture) {
			f.createConversation("conv_w10d_ctx", routeTestOwner)
			f.seedTurns(routeTestOwner, "conv_w10d_ctx", 2)
		}},
		{"checkpoint entries 查询失败", "FROM chat_context_entries", func(f *chatFixture) {
			claim := seedCompactionClaimW10D(t, f, "conv_w10d_ctx")
			if progressed, err := f.store.RecordCompactionProgress(RecordCompactionProgressInput{
				ConversationID: "conv_w10d_ctx", SystemAccountID: routeTestOwner, ClaimID: claim.ClaimID,
				ThroughSequence: 2, EarliestExpiresAt: "2026-03-11T08:00:00.000Z", Now: f.nowISO,
			}); err != nil || !progressed {
				t.Fatalf("推进进度失败: %v %v", progressed, err)
			}
			head, _ := f.store.GetContextHead("conv_w10d_ctx", routeTestOwner)
			if _, err := f.store.InstallContextCheckpoint(installInputW10D(f, "conv_w10d_ctx", claim, head.ContextRevision)); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, item := range loadCases {
		item := item
		runRecoverableFaultCaseW10D(t, "LoadModelContext/"+item.name, func(f *chatFixture, script *faultScriptW10D) {
			item.seed(f)
			script.failOnce(item.substr)
		}, func(f *chatFixture) error {
			_, err := f.store.LoadModelContext("conv_w10d_ctx", routeTestOwner, f.nowISO, 512, 16*1024*1024)
			return err
		})
	}

	t.Run("RecordCompactionProgress/更新失败", func(t *testing.T) {
		fixture, script := newFaultChatFixtureW10D(t)
		claim := seedCompactionClaimW10D(t, fixture, "conv_w10d_ctx")
		script.failOnce("SET context_progress_sequence = ?")
		if _, err := fixture.store.RecordCompactionProgress(RecordCompactionProgressInput{
			ConversationID: "conv_w10d_ctx", SystemAccountID: routeTestOwner, ClaimID: claim.ClaimID,
			ThroughSequence: 2, EarliestExpiresAt: "2026-03-11T08:00:00.000Z", Now: fixture.nowISO,
		}); err == nil {
			t.Fatalf("注入故障未生效")
		}
	})

	t.Run("ReleaseCompactionClaim/更新失败", func(t *testing.T) {
		fixture, script := newFaultChatFixtureW10D(t)
		claim := seedCompactionClaimW10D(t, fixture, "conv_w10d_ctx")
		script.failOnce("SET context_state = 'compact_pending', context_claim_id = NULL")
		if _, err := fixture.store.ReleaseCompactionClaim("conv_w10d_ctx", routeTestOwner, claim.ClaimID, fixture.nowISO); err == nil {
			t.Fatalf("注入故障未生效")
		}
	})

	t.Run("FailCompaction/更新失败", func(t *testing.T) {
		fixture, script := newFaultChatFixtureW10D(t)
		claim := seedCompactionClaimW10D(t, fixture, "conv_w10d_ctx")
		script.failOnce("SET context_state = 'compact_failed'")
		retryAt := "2026-03-11T08:00:00.000Z"
		if _, err := fixture.store.FailCompaction(FailCompactionInput{
			ConversationID: "conv_w10d_ctx", SystemAccountID: routeTestOwner, ClaimID: claim.ClaimID,
			ErrorCode: "chat_ctx_err", RetryAt: &retryAt, Now: fixture.nowISO,
		}); err == nil {
			t.Fatalf("注入故障未生效")
		}
	})

	t.Run("FailPendingCompaction/更新失败", func(t *testing.T) {
		fixture, script := newFaultChatFixtureW10D(t)
		fixture.createConversation("conv_w10d_ctx", routeTestOwner)
		fixture.seedTurns(routeTestOwner, "conv_w10d_ctx", 2)
		head, _ := fixture.store.GetContextHead("conv_w10d_ctx", routeTestOwner)
		if !requestCompaction(t, fixture, "conv_w10d_ctx", routeTestOwner, head.ContextRevision, 2) {
			t.Fatal("请求压缩失败")
		}
		script.failOnce("SET context_state = 'compact_failed'")
		retryAt := "2026-03-11T08:00:00.000Z"
		if _, err := fixture.store.FailPendingCompaction(FailPendingCompactionInput{
			ConversationID: "conv_w10d_ctx", SystemAccountID: routeTestOwner,
			ExpectedRevision: head.ContextRevision, ErrorCode: "chat_ctx_err2", RetryAt: &retryAt, Now: fixture.nowISO,
		}); err == nil {
			t.Fatalf("注入故障未生效")
		}
	})

	// 纯校验臂：errorCode 非法 → DomainError。
	t.Run("FailCompaction/errorCode 非法", func(t *testing.T) {
		fixture, _ := newFaultChatFixtureW10D(t)
		if _, err := fixture.store.FailCompaction(FailCompactionInput{
			ConversationID: "conv_w10d_ctx", SystemAccountID: routeTestOwner, ClaimID: "c",
			ErrorCode: stringsRepeatW10D("x", 200), Now: fixture.nowISO,
		}); err == nil || err.Error() != "errorCode 无效" {
			t.Fatalf("非法 errorCode = %v", err)
		}
	})
	t.Run("FailPendingCompaction/errorCode 非法", func(t *testing.T) {
		fixture, _ := newFaultChatFixtureW10D(t)
		if _, err := fixture.store.FailPendingCompaction(FailPendingCompactionInput{
			ConversationID: "conv_w10d_ctx", SystemAccountID: routeTestOwner,
			ExpectedRevision: 0, ErrorCode: stringsRepeatW10D("y", 200), Now: fixture.nowISO,
		}); err == nil || err.Error() != "errorCode 无效" {
			t.Fatalf("非法 errorCode = %v", err)
		}
	})
}

// 可选：暂不提供 seedFindClaimW10D（已不引用）。
// 占位：本文件不再需要 seedFindClaimW10D 辅助函数。
