package accountkeystates

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestLoadAPIKeyRuntimeDetails 锁定 Node
// loadAccountApiKeyRuntimeDetailsByAccountIdsAsync + accountApiKeyRuntimeDetailsFromRows
// 的投影合同：池展开顺序、指纹前缀/末 4 位掩码、无状态 Key 的 active 兜底、
// 运行态计数透传、空值不出键、非池账户渲染空列表且不泄露明文。
func TestLoadAPIKeyRuntimeDetails(t *testing.T) {
	h := openTestDB(t)
	h.seedSchema(t)
	fingerprints := make([]string, 0, 2)
	h.seedAccount(t, struct {
		id           string
		keyCount     int
		status       string
		schedulable  int
		configRev    int64
		providerCode string
		protocolCode string
		protocolVer  string
		fingerprints *[]string
	}{id: "acc", keyCount: 2, status: "active", schedulable: 1, configRev: 1,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1",
		fingerprints: &fingerprints})
	// Key #1（索引 0）进入 error 态并带计数；Key #2 无运行态行 → active 兜底。
	h.seedState(t, "acc", fingerprints[0], map[string]any{
		"status":               "error",
		"failure_count":        7,
		"consecutive_failures": 3,
		"success_count":        0,
		"last_error_code":      "upstream_5xx",
		"last_error_message":   "  boom   boom  ",
		"last_trace_id":        "trace-1",
		"last_failure_at":      plusMillis(-1000),
	})

	items, err := h.store.LoadAPIKeyRuntimeDetails(context.Background(), "acc")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items: %v", items)
	}
	first := items[0]
	if first["keyIndex"] != 0 || first["status"] != "error" {
		t.Fatalf("first item identity: %v", first)
	}
	if first["keyFingerprintPrefix"] != fingerprints[0][:12] {
		t.Fatalf("fingerprint prefix: %v", first["keyFingerprintPrefix"])
	}
	// Key 形如 sk-test-acc-a / sk-test-acc-b：末 4 位掩码且不泄露明文。
	if suffix, ok := first["keySuffix"].(string); !ok || len(suffix) != 4 || !strings.HasSuffix("sk-test-acc-a", suffix) {
		t.Fatalf("key suffix mask: %v", first["keySuffix"])
	}
	if first["failureCount"] != 7 || first["consecutiveFailures"] != 3 || first["successCount"] != 0 {
		t.Fatalf("counts: %v", first)
	}
	if first["lastErrorCode"] != "upstream_5xx" || first["lastErrorMessage"] != "boom boom" ||
		first["lastTraceId"] != "trace-1" {
		t.Fatalf("failure projection: %v", first)
	}
	second := items[1]
	if second["keyIndex"] != 1 || second["status"] != "active" {
		t.Fatalf("second item identity: %v", second)
	}
	for _, field := range []string{"lastErrorCode", "lastErrorMessage", "lastTraceId", "lastFailureAt", "cooldownUntil", "nextProbeAt"} {
		if _, present := second[field]; present {
			t.Fatalf("stateless key must omit %s: %v", field, second)
		}
	}

	// 脱敏合同：整列表不含完整明文 Key（仅指纹前缀 + 末 4 位）。
	encoded, _ := json.Marshal(items)
	if strings.Contains(string(encoded), "sk-test-acc-a") || strings.Contains(string(encoded), "sk-test-acc-b") {
		t.Fatalf("plaintext key leaked: %s", encoded)
	}

	// 单 Key（非池）/oauth 账户 → 空列表。
	h.seedAccount(t, struct {
		id           string
		keyCount     int
		status       string
		schedulable  int
		configRev    int64
		providerCode string
		protocolCode string
		protocolVer  string
		fingerprints *[]string
	}{id: "acc-single", keyCount: 1, status: "active", schedulable: 1, configRev: 1,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1"})
	if items, err := h.store.LoadAPIKeyRuntimeDetails(context.Background(), "acc-single"); err != nil || len(items) != 0 {
		t.Fatalf("single key pool must render empty: %v %v", items, err)
	}
	if items, err := h.store.LoadAPIKeyRuntimeDetails(context.Background(), "acc-missing"); err != nil || len(items) != 0 {
		t.Fatalf("missing account must render empty: %v %v", items, err)
	}
}

// TestLoadAPIKeyRuntimeDetailsDecryptFailure 锁定解密失败分支：Node 按账户缺失
// 处理（continue → 空列表），不向上传播错误。
func TestLoadAPIKeyRuntimeDetailsDecryptFailure(t *testing.T) {
	h := openTestDB(t)
	h.seedSchema(t)
	h.exec(t, `
    INSERT INTO accounts (id, system_account_id, name, type, status, schedulable,
      provider_code, protocol_code, protocol_version, config_revision, credentials_encrypted)
    VALUES ('acc-bad', 'sys-owner', 'bad', 'api_key', 'active', 1, 'openai', 'openai', 'v1', 1, 'not-an-envelope')`)
	items, err := h.store.LoadAPIKeyRuntimeDetails(context.Background(), "acc-bad")
	if err != nil {
		t.Fatalf("decrypt failure must not propagate: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("decrypt failure must render empty: %v", items)
	}
}
