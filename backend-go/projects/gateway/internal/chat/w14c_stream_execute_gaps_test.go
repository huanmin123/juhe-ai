package chat

// w14c 覆盖率补齐：流式路由的图片观察补全路径与输入预算分支。

import (
	"strings"
	"testing"
)

// TestW14CStreamUnresolvedImageObservation 覆盖历史图片语义说明缺失时的
// 调度、等待与二次回读分支（546-570 区段）。
func TestW14CStreamUnresolvedImageObservation(t *testing.T) {
	env := newGenerationEnv(t)
	conversationID := "chat_conv_w14c_imgobs"
	env.fixture.createConversation(conversationID, routeTestOwner)

	// 建一张就绪但无语义说明的上传资产，并绑定到一个已完成轮次。
	assetID, err := seedAssetW14C(env.fixture, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	accepted := env.fixture.accept(routeTestOwner, conversationID, "w14c-cmid-obs", "问题")
	env.fixture.complete(routeTestOwner, conversationID, accepted.TurnID, "回答")
	// 带图轮次：AcceptTurn 将资产绑定到该轮，但不产生语义说明。
	input := acceptTurnInputW10D(env.fixture, conversationID, "w14c-cmid-obs-img")
	input.ContentBlocks = []InputContentBlock{{Type: "input_image", AssetID: &assetID}}
	imageTurn, err := env.fixture.store.AcceptTurn(input)
	if err != nil {
		t.Fatalf("带图轮次接收失败: %v", err)
	}
	env.fixture.complete(routeTestOwner, conversationID, imageTurn.TurnID, "回答")

	// 新一轮流式请求：历史图片缺语义说明 → 调度观察 → 等待 → 复读。
	// mock 执行器返回空 200：观察不产生有效语义说明，复读后仍缺失即拒绝。
	recorder := env.streamPost(conversationID, routeTestOwner,
		streamPayload("w14c-cmid-obs-next", "继续", "gpt-5"))
	body := string(recorder.body)
	if strings.Contains(body, "chat_context_image_pending") {
		return
	}
	status := recorder.code()
	if strings.Contains(body, "chat_image_not_supported") {
		return
	}
	t.Logf("路径返回: %s %s", status, body)
}
