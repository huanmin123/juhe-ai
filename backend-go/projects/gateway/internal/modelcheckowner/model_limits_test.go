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
	// v16 契约（设计 §8 去供应商化）：限额快照读 models 单表——vendor_code 对
	// 应旧 provider_code，upstream_model 对应旧 model 列。
	if _, err := db.Exec(`CREATE TABLE models (id TEXT PRIMARY KEY, vendor_code TEXT, upstream_model TEXT, scope TEXT, system_account_id TEXT, context_window_tokens INTEGER, max_input_tokens INTEGER, status TEXT, catalog_visible INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO models (id,vendor_code,upstream_model,scope,system_account_id,context_window_tokens,max_input_tokens,status,catalog_visible) VALUES ('m1','gpt','gpt-5.6','builtin',NULL,128000,0,'active',1),('m2','gpt','small','builtin',NULL,8000,4096,'active',1)`); err != nil {
		t.Fatal(err)
	}
	limits, err := NewVersionedModelLimits(db, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := limits.MaxInputTokens("gpt", "gpt-5.6", modelcheckprofile.ProtocolOpenAIResponses); err != nil || got != 128000 {
		t.Fatalf("fallback limit=%d err=%v", got, err)
	}
	if got, err := limits.MaxInputTokens("gpt", "small", modelcheckprofile.ProtocolOpenAIResponses); err != nil || got != 4096 {
		t.Fatalf("explicit limit=%d err=%v", got, err)
	}
	if _, err := limits.MaxInputTokens("gpt", "missing", modelcheckprofile.ProtocolOpenAIResponses); err == nil {
		t.Fatal("missing model limit must fail closed")
	}
}
