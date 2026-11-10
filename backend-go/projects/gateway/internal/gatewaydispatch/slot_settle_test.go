package gatewaydispatch

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
)

type recordingSlotAffinity struct {
	*fakeAffinity
	applied bool
	calls   int
	key     string
	from    string
	to      string
	cap     int
}

func (f *recordingSlotAffinity) PlanSessionSlot(ctx context.Context, sessionAffinityKey, ownerAccountID string, ownerEstablished bool, candidates []SessionSlotCandidate, waitBudgetMs int64) (SessionAffinitySlotPlan, error) {
	return SessionAffinitySlotPlan{}, nil
}

func (f *recordingSlotAffinity) ConfirmSessionSlot(ctx context.Context, sessionAffinityKey, expectedOwner, newOwner string, cap int) bool {
	f.calls++
	f.key = sessionAffinityKey
	f.from = expectedOwner
	f.to = newOwner
	f.cap = cap
	return f.applied
}

type slotConfirmMetadataAudit struct {
	metadata map[string]any
}

func (a *slotConfirmMetadataAudit) BindContext(gatewaypreauth.AuditGatewayContext) {}
func (a *slotConfirmMetadataAudit) AddGatewayMetadata(label string, metadata map[string]any) {
	if label == "session_affinity_slot_confirm" {
		a.metadata = metadata
	}
}
func (a *slotConfirmMetadataAudit) Finalize(gatewaypreauth.AuditFinalizeInput) {}

func TestConfirmSessionAffinitySlotFunc(t *testing.T) {
	ctx := context.Background()
	port := &recordingSlotAffinity{fakeAffinity: &fakeAffinity{}, applied: true}
	capture := &slotConfirmMetadataAudit{}
	fn := newConfirmSessionAffinitySlotFunc(port, ctx, "aff", "a", "b", 10, 60, true, AuditCapture{Context: capture})
	if fn == nil {
		t.Fatal("slot confirm closure must not be nil")
	}
	fn()
	if port.calls != 1 || port.key != "aff" || port.from != "a" || port.to != "b" || port.cap != gatewaysession.SessionSlotCap(10, 60) {
		t.Fatalf("slot confirm call = %+v", port)
	}
	if capture.metadata["slotFallback"] != true || capture.metadata["applied"] != true {
		t.Fatalf("slot confirm metadata = %+v", capture.metadata)
	}

	// 非名额回退同 owner 成功：仍写名额，但 audit 明确 slotFallback=false。
	capture = &slotConfirmMetadataAudit{}
	fn = newConfirmSessionAffinitySlotFunc(port, ctx, "aff", "a", "a", 10, 0, false, AuditCapture{Context: capture})
	if fn == nil {
		t.Fatal("same-owner slot confirm closure must not be nil")
	}
	fn()
	if capture.metadata["slotFallback"] != false {
		t.Fatalf("same-owner metadata = %+v", capture.metadata)
	}

	// 端口未实现名额面：不生成闭包。
	if fn := newConfirmSessionAffinitySlotFunc(&fakeAffinity{}, ctx, "aff", "a", "b", 10, 0, true, AuditCapture{}); fn != nil {
		t.Fatal("port without slot capability must return nil")
	}
}
