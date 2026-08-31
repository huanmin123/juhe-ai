package modelcheckowner

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
	_ "modernc.org/sqlite"
)

func TestResolveConfiguredUpstreamModelMappingPreservesFamilies(t *testing.T) {
	db := newModelMappingDatabase(t)
	defer db.Close()
	profile, ok := modelcheckprofile.Find("openai", "profile_openai_openai_v1")
	if !ok {
		t.Fatal("OpenAI Responses profile is required")
	}
	if _, err := db.Exec(`INSERT INTO account_supported_models(account_id,model) VALUES ('acct-1','gpt-5.6-terra')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_model_mappings(account_id,source_model,source_endpoint_family,upstream_model,upstream_endpoint_family,enabled) VALUES ('acct-1','gpt-5.6-sol','responses','gpt-5.6-terra','chat_completions',1)`); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveConfiguredUpstreamModelMapping(context.Background(), db, false, "acct-1", profile, "gpt-5.6-sol")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.UpstreamModel != "gpt-5.6-terra" || resolved.SourceEndpointFamily != modelcheckprofile.EndpointResponses || resolved.UpstreamEndpointFamily != modelcheckprofile.EndpointChatCompletions {
		t.Fatalf("resolution=%+v", resolved)
	}
}

func TestResolveConfiguredUpstreamModelMappingRejectsUnsupportedCrossFamily(t *testing.T) {
	db := newModelMappingDatabase(t)
	defer db.Close()
	profile, ok := modelcheckprofile.Find("openai", "profile_openai_openai_v1")
	if !ok {
		t.Fatal("OpenAI Responses profile is required")
	}
	if _, err := db.Exec(`INSERT INTO account_supported_models(account_id,model) VALUES ('acct-1','gpt-5.6-terra')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_model_mappings(account_id,source_model,source_endpoint_family,upstream_model,upstream_endpoint_family,enabled) VALUES ('acct-1','gpt-5.6-sol','responses','gpt-5.6-terra','messages',1)`); err != nil {
		t.Fatal(err)
	}
	_, err := resolveConfiguredUpstreamModelMapping(context.Background(), db, false, "acct-1", profile, "gpt-5.6-sol")
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported cross-family mapping must fail closed, err=%v", err)
	}
}

func TestResolveConfiguredUpstreamModelMappingAcceptsSameFamily(t *testing.T) {
	db := newModelMappingDatabase(t)
	defer db.Close()
	profile, ok := modelcheckprofile.Find("openai", "profile_openai_openai_v1")
	if !ok {
		t.Fatal("OpenAI Responses profile is required")
	}
	if _, err := db.Exec(`INSERT INTO account_supported_models(account_id,model) VALUES ('acct-1','gpt-5.6-terra')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_model_mappings(account_id,source_model,source_endpoint_family,upstream_model,upstream_endpoint_family,enabled) VALUES ('acct-1','gpt-5.6-sol','responses','gpt-5.6-terra','responses',1)`); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveConfiguredUpstreamModelMapping(context.Background(), db, false, "acct-1", profile, "gpt-5.6-sol")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.UpstreamModel != "gpt-5.6-terra" || resolved.SourceEndpointFamily != modelcheckprofile.EndpointResponses || resolved.UpstreamEndpointFamily != modelcheckprofile.EndpointResponses {
		t.Fatalf("resolution=%+v", resolved)
	}
}

func TestResolveConfiguredUpstreamModelMappingReadsMappingWithoutModelRestriction(t *testing.T) {
	db := newModelMappingDatabase(t)
	defer db.Close()
	profile, ok := modelcheckprofile.Find("openai", "profile_openai_openai_v1")
	if !ok {
		t.Fatal("OpenAI Responses profile is required")
	}
	if _, err := db.Exec(`INSERT INTO account_model_mappings(account_id,source_model,source_endpoint_family,upstream_model,upstream_endpoint_family,enabled) VALUES ('acct-1','gpt-5.6-sol','responses','gpt-5.6-terra','chat_completions',1)`); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveConfiguredUpstreamModelMapping(context.Background(), db, false, "acct-1", profile, "gpt-5.6-sol")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.UpstreamModel != "gpt-5.6-terra" || resolved.UpstreamEndpointFamily != modelcheckprofile.EndpointChatCompletions {
		t.Fatalf("mapping must apply when supported model list is empty: %+v", resolved)
	}
}

func TestResolveConfiguredUpstreamModelMappingMappingPrecedesDirectMatch(t *testing.T) {
	db := newModelMappingDatabase(t)
	defer db.Close()
	profile, ok := modelcheckprofile.Find("openai", "profile_openai_openai_v1")
	if !ok {
		t.Fatal("OpenAI Responses profile is required")
	}
	if _, err := db.Exec(`INSERT INTO account_supported_models(account_id,model) VALUES ('acct-1','gpt-5.6-sol'),('acct-1','gpt-5.6-terra')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_model_mappings(account_id,source_model,source_endpoint_family,upstream_model,upstream_endpoint_family,enabled) VALUES ('acct-1','gpt-5.6-sol','responses','gpt-5.6-terra','chat_completions',1)`); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveConfiguredUpstreamModelMapping(context.Background(), db, false, "acct-1", profile, "gpt-5.6-sol")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.UpstreamModel != "gpt-5.6-terra" || resolved.UpstreamEndpointFamily != modelcheckprofile.EndpointChatCompletions {
		t.Fatalf("explicit mapping must precede direct match: %+v", resolved)
	}
}

func newModelMappingDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/model-mapping.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{
		`CREATE TABLE account_supported_models (account_id TEXT,model TEXT)`,
		`CREATE TABLE account_model_mappings (account_id TEXT,source_model TEXT,source_endpoint_family TEXT,upstream_model TEXT,upstream_endpoint_family TEXT,enabled INTEGER)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	return db
}
