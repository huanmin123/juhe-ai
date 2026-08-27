package modelcheckprobe

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

func TestRunBehaviorEvaluatesCredentialFreeConstraints(t *testing.T) {
	item, err := RunBehavior(context.Background(), modelcheckprofile.ProtocolOpenAIChat, "gpt-5.6-sol", func(_ context.Context, request Request) (Result, error) {
		return Result{Success: true, Output: "QUARTZ"}, nil
	})
	if err != nil || item.Kind != "behavior_probe" || item.MaxScore != 35 || item.Score == 0 {
		t.Fatalf("item=%#v err=%v", item, err)
	}
}
