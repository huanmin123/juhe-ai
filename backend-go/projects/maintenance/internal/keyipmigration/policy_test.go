package keyipmigration

import (
	"database/sql"
	"testing"
)

func nullPolicy(text string) sql.NullString {
	return sql.NullString{String: text, Valid: text != ""}
}

func TestParseStoredPolicy(t *testing.T) {
	valid := []struct {
		name  string
		raw   sql.NullString
		soft  int
		ip    int
		hasIP bool
		null  bool
	}{
		{name: "NULL", raw: nullPolicy(""), soft: -1, null: true},
		{name: "单键关闭", raw: nullPolicy(`{"defaultSoftConcurrency":-1}`), soft: -1},
		{name: "单键阈值原样保留5000", raw: nullPolicy(`{"defaultSoftConcurrency":5000}`), soft: 5000},
		{name: "两键", raw: nullPolicy(`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":8}`), soft: 5, ip: 8, hasIP: true},
		{name: "IP为0", raw: nullPolicy(`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":0}`), soft: 5, ip: 0, hasIP: true},
		{name: "键序无关", raw: nullPolicy(`{"clientIpConcurrencyLimit":3,"defaultSoftConcurrency":1}`), soft: 1, ip: 3, hasIP: true},
	}
	for _, tt := range valid {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseStoredPolicy(tt.raw)
			if got.Reason != "" {
				t.Fatalf("意外 needs_review: %s", got.Reason)
			}
			if got.Null != tt.null || got.Soft != tt.soft || got.IP != tt.ip || got.HasIP != tt.hasIP {
				t.Fatalf("解析 = %+v, want null=%t soft=%d ip=%d hasIP=%t", got, tt.null, tt.soft, tt.ip, tt.hasIP)
			}
		})
	}

	invalid := []struct {
		name string
		raw  string
		want string
	}{
		{name: "损坏JSON", raw: `{"defaultSoftConcurrency":5`, want: ReviewMalformedJSON},
		{name: "非对象", raw: `[1]`, want: ReviewMalformedJSON},
		{name: "空对象", raw: `{}`, want: ReviewMissingDefaultSoftConcurrency},
		{name: "仅IP键", raw: `{"clientIpConcurrencyLimit":5}`, want: ReviewMissingDefaultSoftConcurrency},
		{name: "未知键", raw: `{"defaultSoftConcurrency":5,"mode":"balanced_fast"}`, want: ReviewUnknownKey},
		{name: "阈值0", raw: `{"defaultSoftConcurrency":0}`, want: ReviewInvalidDefaultSoftConcurrency},
		{name: "阈值越界", raw: `{"defaultSoftConcurrency":1000001}`, want: ReviewInvalidDefaultSoftConcurrency},
		{name: "阈值小数", raw: `{"defaultSoftConcurrency":5.5}`, want: ReviewInvalidDefaultSoftConcurrency},
		{name: "IP负数", raw: `{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":-1}`, want: ReviewInvalidClientIPConcurrency},
		{name: "IP越界", raw: `{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":1000001}`, want: ReviewInvalidClientIPConcurrency},
		{name: "IP小数", raw: `{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":1.5}`, want: ReviewInvalidClientIPConcurrency},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseStoredPolicy(nullPolicy(tt.raw)); got.Reason != tt.want {
				t.Fatalf("reason = %q, want %q", got.Reason, tt.want)
			}
		})
	}
}

func TestDecideKeyTarget(t *testing.T) {
	tests := []struct {
		name     string
		current  int
		observed []int
		policy   ConflictPolicy
		want     int
		action   string
		reason   string
		conflict bool
	}{
		{name: "无值-1", current: -1, policy: ConflictPolicyBlock, want: -1, action: ActionUnchanged},
		{name: "未发布版本0归一为-1", current: 0, policy: ConflictPolicyBlock, want: -1, action: ActionUpdate},
		{name: "无值preexisting保留", current: 5, policy: ConflictPolicyBlock, want: 5, action: ActionPreexisting},
		{name: "唯一值写入", current: -1, observed: []int{8}, policy: ConflictPolicyBlock, want: 8, action: ActionUpdate},
		{name: "零值按未设置填充", current: 0, observed: []int{8}, policy: ConflictPolicyBlock, want: 8, action: ActionUpdate},
		{name: "唯一值相等", current: 8, observed: []int{8}, policy: ConflictPolicyBlock, want: 8, action: ActionPreexisting},
		{name: "唯一值冲突block", current: 5, observed: []int{8}, policy: ConflictPolicyBlock, want: 5, action: ActionNeedsReview, reason: ReviewExistingKeyLimitConflict},
		{name: "唯一值冲突min保留", current: 5, observed: []int{8}, policy: ConflictPolicyMin, want: 5, action: ActionPreexisting, conflict: true},
		{name: "多值block", current: -1, observed: []int{5, 10}, policy: ConflictPolicyBlock, want: -1, action: ActionNeedsReview, reason: ReviewMultipleIPLimits},
		{name: "多值min取最小", current: -1, observed: []int{10, 5}, policy: ConflictPolicyMin, want: 5, action: ActionUpdate, conflict: true},
		{name: "多值人工值命中观测值不阻断", current: 10, observed: []int{5, 10}, policy: ConflictPolicyBlock, want: 10, action: ActionPreexisting},
		{name: "多值人工值命中非最小值也不阻断", current: 5, observed: []int{5, 10}, policy: ConflictPolicyBlock, want: 5, action: ActionPreexisting},
		{name: "多值人工值全部不同block", current: 7, observed: []int{5, 10}, policy: ConflictPolicyBlock, want: 7, action: ActionNeedsReview, reason: ReviewExistingKeyLimitConflict},
		{name: "多值人工值全部不同min保留", current: 7, observed: []int{5, 10}, policy: ConflictPolicyMin, want: 7, action: ActionPreexisting, conflict: true},
		{name: "重复观测值去重", current: -1, observed: []int{5, 5}, policy: ConflictPolicyBlock, want: 5, action: ActionUpdate},
		{name: "非法负值阻断", current: -2, observed: []int{5}, policy: ConflictPolicyBlock, want: -2, action: ActionNeedsReview, reason: ReviewInvalidKeyLimitValue},
		{name: "非法越界值阻断", current: 1000001, policy: ConflictPolicyBlock, want: 1000001, action: ActionNeedsReview, reason: ReviewInvalidKeyLimitValue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecideKeyTarget(tt.current, tt.observed, tt.policy)
			if got.Target != tt.want || got.Action != tt.action || got.Reason != tt.reason {
				t.Fatalf("decision = %+v, want target=%d action=%s reason=%s", got, tt.want, tt.action, tt.reason)
			}
			if (got.Conflict != nil) != tt.conflict {
				t.Fatalf("conflict = %+v, want present=%t", got.Conflict, tt.conflict)
			}
		})
	}
}

func TestSingleKeyPolicyJSON(t *testing.T) {
	if got := SingleKeyPolicyJSON(5); got != `{"defaultSoftConcurrency":5}` {
		t.Fatalf("单键 JSON = %s", got)
	}
	if got := SingleKeyPolicyJSON(-1); got != "" {
		t.Fatalf("soft=-1 应返回空串（SQL NULL），got %q", got)
	}
}

func TestNormalizeConflictPolicy(t *testing.T) {
	for raw, want := range map[string]ConflictPolicy{
		"":        ConflictPolicyBlock,
		"block":   ConflictPolicyBlock,
		" BLOCK ": ConflictPolicyBlock,
		"min":     ConflictPolicyMin,
		"MIN":     ConflictPolicyMin,
	} {
		got, ok := NormalizeConflictPolicy(raw)
		if !ok || got != want {
			t.Fatalf("NormalizeConflictPolicy(%q) = %q/%t, want %q", raw, got, ok, want)
		}
	}
	if _, ok := NormalizeConflictPolicy("max"); ok {
		t.Fatal("非法 conflict policy 必须拒绝")
	}
}
