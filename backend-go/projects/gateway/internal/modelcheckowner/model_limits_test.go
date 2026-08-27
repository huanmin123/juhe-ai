package modelcheckowner

import (
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
	_ "modernc.org/sqlite"
)

func TestVersionedModelLimitsReadsCatalogFallback(t *testing.T) {
	db, err := sql.Open("sqlite", "file:model-limits?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE provider_model_catalog (provider_code TEXT, model TEXT, context_window_tokens INTEGER, max_input_tokens INTEGER, status TEXT, catalog_visible INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO provider_model_catalog VALUES ('openai','gpt-5.6',128000,0,'active',1),('openai','small',8000,4096,'active',1)`); err != nil {
		t.Fatal(err)
	}
	limits, err := NewVersionedModelLimits(db, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := limits.MaxInputTokens("openai", "gpt-5.6", modelcheckprofile.ProtocolOpenAIResponses); err != nil || got != 128000 {
		t.Fatalf("fallback limit=%d err=%v", got, err)
	}
	if got, err := limits.MaxInputTokens("openai", "small", modelcheckprofile.ProtocolOpenAIResponses); err != nil || got != 4096 {
		t.Fatalf("explicit limit=%d err=%v", got, err)
	}
	if _, err := limits.MaxInputTokens("openai", "missing", modelcheckprofile.ProtocolOpenAIResponses); err == nil {
		t.Fatal("missing model limit must fail closed")
	}
}
