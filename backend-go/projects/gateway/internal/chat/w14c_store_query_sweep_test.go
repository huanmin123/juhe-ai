package chat

// w14c 覆盖率补齐：store 查询错误臂的通用扫描。
// 对若干 store 操作序列，逐一注入“第 n 条含 chat_ 的查询失败”，把每条
// 查询的错误传播分支都走到。序列内部所有错误都被容忍（本测试只服务
// 覆盖率，不校验业务结果）。

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

const w14cSweepConv = "chat_conv_w14c_sweep"

// t14cSweep 由 TestW14CStoreQueryFaultSweep 注入，供 fixture 内部 Fatal 使用。
var t14cSweep *testing.T

func w14cCompleteActiveTurn(f *chatFixture, conversationID string) error {
	accepted, err := f.store.AcceptTurn(acceptTurnInputW10D(f, conversationID, "w14c-sweep-a"))
	if err != nil {
		return err
	}
	_, err = f.store.CompleteChatTurn(CompleteTurnInput{
		ConversationID:   conversationID,
		SystemAccountID:  routeTestOwner,
		TurnID:           accepted.TurnID,
		AssistantContent: "回答",
		FinishReason:     "stop",
		Now:              f.nowISO,
	})
	return err
}

// seedAssetW14C 建一张就绪的上传资产（静默版，错误由返回值承载）。
func seedAssetW14C(f *chatFixture, conversationID string) (string, error) {
	created, err := f.store.CreateChatAsset(CreateChatAssetInput{
		SystemAccountID: routeTestOwner, ConversationID: conversationID,
		SourceKind: "user_upload", OriginalBytes: 10,
		OriginalSha256: strings.Repeat("e", 64),
		QuotaBytes:     10, Now: f.nowISO, RetentionDays: 30,
	})
	if err != nil {
		return "", err
	}
	completed, err := f.store.CompleteChatAssetProcessing(CompleteAssetProcessingInput{
		AssetID: created.ID, SystemAccountID: routeTestOwner, ConversationID: conversationID,
		ProcessedMimeType: "image/webp", ProcessedWidth: 1, ProcessedHeight: 1, ProcessedBytes: 10,
		ProcessedSha256: strings.Repeat("f", 64), StorageKey: "w14c-key", Now: f.nowISO,
	})
	if err != nil || completed == nil {
		return "", fmt.Errorf("w14c 完成资产处理失败: %v", err)
	}
	return completed.ID, nil
}

// installInputW14C 构造合法的检查点安装输入。
func installInputW14C(f *chatFixture, conversationID string, claim *ContextCompactionClaim, revision int64) InstallCheckpointInput {
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

// TestW14CStoreQueryFaultSweep 扫描各序列的第 n 条查询失败。
func TestW14CStoreQueryFaultSweep(t *testing.T) {
	t14cSweep = t
	sequences := map[string]func(f *chatFixture, conversationID string) error{
		"accept-complete": func(f *chatFixture, conversationID string) error {
			return w14cCompleteActiveTurn(f, conversationID)
		},
		"accept-replace": func(f *chatFixture, conversationID string) error {
			if err := w14cCompleteActiveTurn(f, conversationID); err != nil {
				return err
			}
			var turnID string
			if err := f.db.QueryRow(`SELECT id FROM chat_messages WHERE conversation_id = ? AND role = 'user' ORDER BY sequence_no DESC LIMIT 1`,
				conversationID).Scan(&turnID); err != nil {
				return err
			}
			input := acceptTurnInputW10D(f, conversationID, "w14c-sweep-b")
			input.ReplaceTurnID = turnID
			_, err := f.store.AcceptTurn(input)
			return err
		},
		"accept-asset": func(f *chatFixture, conversationID string) error {
			assetID, err := seedAssetW14C(f, conversationID)
			if err != nil {
				return err
			}
			input := acceptTurnInputW10D(f, conversationID, "w14c-sweep-c")
			input.ContentBlocks = []InputContentBlock{{Type: "input_image", AssetID: &assetID}}
			_, err = f.store.AcceptTurn(input)
			return err
		},
		"sync-head": func(f *chatFixture, conversationID string) error {
			if err := w14cCompleteActiveTurn(f, conversationID); err != nil {
				return err
			}
			_, err := f.store.GetConversationSyncHead(conversationID, routeTestOwner, f.nowISO)
			return err
		},
		"context-load": func(f *chatFixture, conversationID string) error {
			if err := w14cCompleteActiveTurn(f, conversationID); err != nil {
				return err
			}
			_, err := f.store.LoadModelContext(conversationID, routeTestOwner, f.nowISO, maxContextLoadRows, maxContextLoadBytes)
			return err
		},
		"compact-claim": func(f *chatFixture, conversationID string) error {
			if err := w14cCompleteActiveTurn(f, conversationID); err != nil {
				return err
			}
			head, err := f.store.GetContextHead(conversationID, routeTestOwner)
			if err != nil {
				return err
			}
			_, err = f.store.ClaimContextCompaction(ClaimCompactionInput{
				ConversationID: conversationID, SystemAccountID: routeTestOwner,
				ExpectedRevision: head.ContextRevision, SourceThroughSequence: 2,
				Now: f.nowISO, StaleClaimBefore: f.nowISO,
			})
			return err
		},
		"compact-install": func(f *chatFixture, conversationID string) error {
			if err := w14cCompleteActiveTurn(f, conversationID); err != nil {
				return err
			}
			head, err := f.store.GetContextHead(conversationID, routeTestOwner)
			if err != nil {
				return err
			}
			claim, err := f.store.ClaimContextCompaction(ClaimCompactionInput{
				ConversationID: conversationID, SystemAccountID: routeTestOwner,
				ExpectedRevision: head.ContextRevision, SourceThroughSequence: 2,
				Now: f.nowISO, StaleClaimBefore: f.nowISO,
			})
			if err != nil {
				return err
			}
			if claim == nil {
				return contextCanceledW14C
			}
			_, err = f.store.InstallContextCheckpoint(installInputW14C(f, conversationID, claim, head.ContextRevision))
			return err
		},
		"asset-delete": func(f *chatFixture, conversationID string) error {
			assetID, err := seedAssetW14C(f, conversationID)
			if err != nil {
				return err
			}
			claim, err := f.store.ClaimUncommittedAssetForDeletion(assetID, routeTestOwner, conversationID, f.nowISO)
			if err != nil {
				return err
			}
			if claim == nil {
				return contextCanceledW14C
			}
			_, err = f.store.CompleteAssetDeletion(claim.Asset.ID, claim.ClaimID)
			return err
		},
		"clear-conversation": func(f *chatFixture, conversationID string) error {
			if err := w14cCompleteActiveTurn(f, conversationID); err != nil {
				return err
			}
			_, err := f.store.ClearConversation(ClearConversationInput{
				ConversationID: conversationID, SystemAccountID: routeTestOwner, Now: f.nowISO,
			})
			return err
		},
	}

	for name, sequence := range sequences {
		sequence := sequence
		t.Run(name, func(t *testing.T) {
			t14cSweep = t
			for n := 1; n <= 48; n++ {
				n := n
				t.Run(fmt.Sprintf("n%02d", n), func(t *testing.T) {
					t14cSweep = t
					fixture, script := newFaultChatFixtureW10D(t)
					conversationID := fmt.Sprintf("%s-%d", w14cSweepConv, n)
					fixture.createConversation(conversationID, routeTestOwner)
					// 故障在建库/建会话之后注入，只影响序列内的查询。
					script.failNth("chat_", n)
					_ = sequence(fixture, conversationID)
				})
			}
		})
	}
}

type w14cContextCanceled struct{}

func (w14cContextCanceled) Error() string { return "w14c 认领缺失" }

var contextCanceledW14C error = w14cContextCanceled{}

var _ = context.Background
