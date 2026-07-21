package cooldownaccountretest

import (
	"context"
	"testing"

	module "juhe-ai/backend-go/internal/modules/cooldownaccountretest"
	"juhe-ai/backend-go/internal/store/port"
)

func TestHandleTaskRejectsInvalidPayload(t *testing.T) {
	err := HandleTask(context.Background(), module.Processor{}, []byte(`{bad`))
	if err == nil {
		t.Fatal("expected payload error")
	}
}

func TestEncodeTaskRequiresStableIdentity(t *testing.T) {
	if _, err := EncodeTask(port.CooldownAccountRetestTask{AccountID: "acct", ConfigRevision: 0}); err == nil {
		t.Fatal("expected config revision error")
	}
}
