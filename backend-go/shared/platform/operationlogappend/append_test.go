package operationlogappend

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNormalizeMatchesF4ProducerShape(t *testing.T) {
	createdAt := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	input, err := normalize(Input{
		ID: "oplog_test", ActorSystemAccountID: "admin-1", ActorRole: "admin",
		Mode: "admin", Module: "proxies", Action: "test", OperationKey: "proxies.test",
		ResourceType: "proxy", ResourceID: "proxy-1", ResourceName: "东京", Summary: "检测代理：东京",
		DetailLevel: "full", VisibilityScope: "admin_only", Metadata: json.RawMessage(`{}`), CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Targets) != 1 || input.Targets[0].Relation != "primary" || input.Targets[0].TargetID != "proxy-1" {
		t.Fatalf("primary target=%#v", input.Targets)
	}
	if len(input.Viewers) != 0 {
		t.Fatalf("admin-only input must not add viewers: %#v", input.Viewers)
	}
}

func TestSearchTermsUsesF4NFKCNormalization(t *testing.T) {
	terms := searchTerms("ＡＢＣ 代理")
	seen := make(map[string]bool, len(terms))
	for _, term := range terms {
		seen[term] = true
	}
	if !seen["abc 代理"] || !seen["abc代理"] {
		t.Fatalf("NFKC-normalized terms missing: %#v", terms)
	}
}
