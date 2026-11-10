package groupspolicymigration

import (
	"database/sql"
	"testing"
)

// 映射与分类纯函数的表驱动基线（普通分组可选调度策略设计 §2.2/§5.2；无外部
// 依赖）。16 键旧文档样例取自退役前的 mockHighConcurrencyPolicy 全集形状。

func nullPolicy(text string) sql.NullString {
	if text == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: text, Valid: true}
}

func TestNormalizeRowLegacyHighConcurrencyMapping(t *testing.T) {
	const fullLegacy = `{"mode":"balanced_fast","defaultSoftConcurrency":5000,"fastFirstEnabled":true,
		"fallbackOnQueueEnabled":true,"breakAffinityOnSoftLimit":true,"breakAffinityOnQueueWaitMs":0,
		"slowRequestThresholdMs":30000,"firstOutputSlowThresholdMs":15000,"recentTimeoutWindowSeconds":120,
		"recentTimeoutPenaltyThreshold":2,"maxQueueWaitMs":60000,"maxQueueSize":5000,"perApiKeyQueueLimit":5000,
		"clientIpConcurrencyLimit":0,"clientIpConcurrencyOverflowMode":"reject","imageLaneMaxConcurrency":0}`
	tests := []struct {
		name          string
		groupType     string
		policy        string
		globalMax     int
		soft          int
		ip            int
		targetNull    bool
		queueOverflow bool
		imageLane     bool
	}{
		{name: "旧默认globalMax等价硬上限映射为关闭", groupType: LegacyGroupTypeHighConcurrency, policy: fullLegacy, globalMax: 5000,
			soft: -1, ip: 0, targetNull: true},
		{name: "显式阈值保留且标注queue行为变化", groupType: LegacyGroupTypeHighConcurrency,
			policy: `{"mode":"balanced_fast","defaultSoftConcurrency":12,"fastFirstEnabled":true,"fallbackOnQueueEnabled":true,
			"breakAffinityOnSoftLimit":true,"breakAffinityOnQueueWaitMs":0,"slowRequestThresholdMs":30000,
			"firstOutputSlowThresholdMs":15000,"recentTimeoutWindowSeconds":120,"recentTimeoutPenaltyThreshold":2,
			"maxQueueWaitMs":45000,"maxQueueSize":5000,"perApiKeyQueueLimit":5000,"clientIpConcurrencyLimit":8,
			"clientIpConcurrencyOverflowMode":"queue","imageLaneMaxConcurrency":6}`,
			globalMax: 5000, soft: 12, ip: 8, queueOverflow: true, imageLane: true},
		{name: "阈值等于globalMax映射为关闭", groupType: LegacyGroupTypeHighConcurrency,
			policy:    `{"defaultSoftConcurrency":5000,"clientIpConcurrencyLimit":5,"maxQueueWaitMs":60000}`,
			globalMax: 5000, soft: -1, ip: 5},
		{name: "阈值大于globalMax映射为关闭", groupType: LegacyGroupTypeHighConcurrency,
			policy:    `{"defaultSoftConcurrency":9000,"clientIpConcurrencyLimit":0,"maxQueueWaitMs":60000}`,
			globalMax: 5000, soft: -1, ip: 0, targetNull: true},
		{name: "阈值零映射为关闭", groupType: LegacyGroupTypeHighConcurrency,
			policy:    `{"defaultSoftConcurrency":0,"clientIpConcurrencyLimit":3,"maxQueueWaitMs":60000}`,
			globalMax: 5000, soft: -1, ip: 3},
		{name: "阈值负数映射为关闭", groupType: LegacyGroupTypeHighConcurrency,
			policy:    `{"defaultSoftConcurrency":-2,"clientIpConcurrencyLimit":3,"maxQueueWaitMs":60000}`,
			globalMax: 5000, soft: -1, ip: 3},
		{name: "阈值缺失映射为关闭", groupType: LegacyGroupTypeHighConcurrency,
			policy: `{"clientIpConcurrencyLimit":3,"maxQueueWaitMs":60000}`, globalMax: 5000, soft: -1, ip: 3},
		{name: "JSON数值5.0按整数5保留", groupType: LegacyGroupTypeHighConcurrency,
			policy:    `{"defaultSoftConcurrency":5.0,"clientIpConcurrencyLimit":5,"maxQueueWaitMs":60000}`,
			globalMax: 5000, soft: 5, ip: 5},
		{name: "globalMax覆盖时以显式值为准", groupType: LegacyGroupTypeHighConcurrency,
			policy:    `{"defaultSoftConcurrency":100,"clientIpConcurrencyLimit":0,"mode":"balanced_fast"}`,
			globalMax: 200, soft: 100, ip: 0, targetNull: false},
		{name: "阈值恰等于1e6上限映射为关闭（开区间）", groupType: LegacyGroupTypeHighConcurrency,
			policy:    `{"defaultSoftConcurrency":1000000,"clientIpConcurrencyLimit":0,"mode":"balanced_fast"}`,
			globalMax: 2000000, soft: -1, ip: 0, targetNull: true},
		{name: "IP上限1e6保留", groupType: LegacyGroupTypeHighConcurrency,
			policy:    `{"defaultSoftConcurrency":-1,"clientIpConcurrencyLimit":1000000,"mode":"balanced_fast"}`,
			globalMax: 5000, soft: -1, ip: 1000000, targetNull: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, reason := NormalizeRow(tt.groupType, nullPolicy(tt.policy), tt.globalMax)
			if reason != "" {
				t.Fatalf("NormalizeRow 返回 needs_review %q，期望正常映射", reason)
			}
			if target.TargetType != GroupTypePersonal {
				t.Fatalf("目标类型 = %q, want personal", target.TargetType)
			}
			if target.TargetNull != tt.targetNull {
				t.Fatalf("TargetNull = %t, want %t", target.TargetNull, tt.targetNull)
			}
			if target.TargetPolicy.SoftConcurrency != tt.soft || target.TargetPolicy.ClientIPLimit != tt.ip {
				t.Fatalf("目标两键 = (%d, %d), want (%d, %d)",
					target.TargetPolicy.SoftConcurrency, target.TargetPolicy.ClientIPLimit, tt.soft, tt.ip)
			}
			if target.QueueOverflow != tt.queueOverflow {
				t.Fatalf("QueueOverflow = %t, want %t", target.QueueOverflow, tt.queueOverflow)
			}
			if target.ImageLane != tt.imageLane {
				t.Fatalf("ImageLane = %t, want %t", target.ImageLane, tt.imageLane)
			}
		})
	}
}

func TestNormalizeRowNeedsReview(t *testing.T) {
	tests := []struct {
		name      string
		groupType string
		policy    string
		reason    string
	}{
		{name: "损坏JSON", groupType: LegacyGroupTypeHighConcurrency, policy: `{"defaultSoftConcurrency":`, reason: ReviewMalformedJSON},
		{name: "非对象JSON", groupType: LegacyGroupTypeHighConcurrency, policy: `[1,2,3]`, reason: ReviewMalformedJSON},
		{name: "未知键", groupType: LegacyGroupTypeHighConcurrency,
			policy: `{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":5,"customKey":1}`, reason: ReviewUnknownKey},
		{name: "阈值非整数", groupType: LegacyGroupTypeHighConcurrency,
			policy: `{"defaultSoftConcurrency":5.5,"clientIpConcurrencyLimit":5}`, reason: ReviewInvalidValue},
		{name: "阈值字符串", groupType: LegacyGroupTypeHighConcurrency,
			policy: `{"defaultSoftConcurrency":"5","clientIpConcurrencyLimit":5}`, reason: ReviewInvalidValue},
		{name: "IP限制越界", groupType: LegacyGroupTypeHighConcurrency,
			policy: `{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":1000001}`, reason: ReviewInvalidValue},
		{name: "IP限制负数", groupType: LegacyGroupTypeHighConcurrency,
			policy: `{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":-1}`, reason: ReviewInvalidValue},
		{name: "高并发行策略列NULL", groupType: LegacyGroupTypeHighConcurrency, policy: "", reason: ReviewMissingPolicy},
		{name: "未知分组类型", groupType: "team", policy: `{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":5}`, reason: ReviewUnknownGroupType},
		{name: "未知分组类型NULL策略", groupType: "team", policy: "", reason: ReviewUnknownGroupType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, reason := NormalizeRow(tt.groupType, nullPolicy(tt.policy), 5000)
			if reason != tt.reason {
				t.Fatalf("needs_review 原因 = %q, want %q", reason, tt.reason)
			}
		})
	}
}

func TestNormalizeRowPersonalRows(t *testing.T) {
	tests := []struct {
		name       string
		policy     string
		globalMax  int
		targetNull bool
		soft       int
		ip         int
	}{
		{name: "personalNULL策略保持关闭", policy: "", targetNull: true, soft: -1, ip: 0},
		{name: "personal合规两键行", policy: `{"clientIpConcurrencyLimit":5,"defaultSoftConcurrency":5}`, soft: 5, ip: 5},
		{name: "personal两键加旧键读兼容", policy: `{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":5,"maxQueueWaitMs":60000}`, soft: 5, ip: 5},
		{name: "personal空对象目标为NULL（apply重写）", policy: `{}`, targetNull: true, soft: -1, ip: 0},
		{name: "personal显式默认两键目标为NULL（apply重写）", policy: `{"defaultSoftConcurrency":-1,"clientIpConcurrencyLimit":0}`, targetNull: true, soft: -1, ip: 0},
		{name: "personal阈值5000按globalMax映射关闭", policy: `{"defaultSoftConcurrency":5000,"clientIpConcurrencyLimit":5,"mode":"balanced_fast"}`, soft: -1, ip: 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, reason := NormalizeRow(GroupTypePersonal, nullPolicy(tt.policy), tt.globalMax)
			if reason != "" {
				t.Fatalf("NormalizeRow 返回 needs_review %q", reason)
			}
			if target.TargetNull != tt.targetNull {
				t.Fatalf("TargetNull = %t, want %t", target.TargetNull, tt.targetNull)
			}
			if target.TargetPolicy.SoftConcurrency != tt.soft || target.TargetPolicy.ClientIPLimit != tt.ip {
				t.Fatalf("目标两键 = (%d, %d), want (%d, %d)",
					target.TargetPolicy.SoftConcurrency, target.TargetPolicy.ClientIPLimit, tt.soft, tt.ip)
			}
		})
	}
}

func TestRowMatchesTargetIdempotentShapes(t *testing.T) {
	target, reason := NormalizeRow(LegacyGroupTypeHighConcurrency,
		nullPolicy(`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":5,"maxQueueWaitMs":60000}`), 5000)
	if reason != "" || target.TargetNull {
		t.Fatalf("NormalizeRow = %q/null=%t", reason, target.TargetNull)
	}
	// 键序与空白差异不触发重写（语义等值即 unchanged，幂等重跑零写入）。
	if !rowMatchesTarget(GroupTypePersonal, nullPolicy(`{"defaultSoftConcurrency":5,  "clientIpConcurrencyLimit":5}`), target) {
		t.Fatal("键序不同的等值两键应判定 unchanged")
	}
	if rowMatchesTarget(GroupTypePersonal, nullPolicy(`{"defaultSoftConcurrency":6,"clientIpConcurrencyLimit":5}`), target) {
		t.Fatal("值不同的两键应判定需要改写")
	}
	if rowMatchesTarget(LegacyGroupTypeHighConcurrency, nullPolicy(`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":5}`), target) {
		t.Fatal("group_type 未归一前不应判定 unchanged")
	}
	// 目标 NULL：任何非空策略（含退役键与空对象）都需要改写为 NULL——空对象
	// 不是合法两键策略，不能当作语义 NULL 跳过。
	nullTarget := RowTarget{TargetType: GroupTypePersonal, TargetNull: true}
	if rowMatchesTarget(GroupTypePersonal, nullPolicy(`{"defaultSoftConcurrency":5000,"mode":"balanced_fast"}`), nullTarget) {
		t.Fatal("含退役键的对象应改写为 NULL")
	}
	if rowMatchesTarget(GroupTypePersonal, nullPolicy(`{}`), nullTarget) {
		t.Fatal("空对象不是合法两键策略，应改写为 NULL")
	}
	if rowMatchesTarget(GroupTypePersonal, nullPolicy(`{"defaultSoftConcurrency":-1,"clientIpConcurrencyLimit":0}`), nullTarget) {
		t.Fatal("显式默认两键对象应改写为 NULL（默认组合存储恒 NULL）")
	}
}

func TestTwoKeyPolicyTargetJSONShape(t *testing.T) {
	encoded := (TwoKeyPolicy{SoftConcurrency: 5, ClientIPLimit: 8}).TargetJSON()
	// map 序列化键序与 gateway 写面 schedulingPolicyJSON 的 json.Marshal 同形
	//（字母序）。
	if encoded != `{"clientIpConcurrencyLimit":8,"defaultSoftConcurrency":5}` {
		t.Fatalf("两键 JSON 形状 = %s", encoded)
	}
}

func TestValidateStoredTwoKeyPolicy(t *testing.T) {
	valid := []string{
		``,
		`{"clientIpConcurrencyLimit":5,"defaultSoftConcurrency":5}`,
		`{"clientIpConcurrencyLimit":0,"defaultSoftConcurrency":-1}`,
		`{"clientIpConcurrencyLimit":1000000,"defaultSoftConcurrency":1000000}`,
	}
	for _, text := range valid {
		if !ValidateStoredTwoKeyPolicy(nullPolicy(text)) {
			t.Fatalf("%q 应判定完成态合规", text)
		}
	}
	invalid := []string{
		`{}`,
		`{"defaultSoftConcurrency":5}`,
		`{"clientIpConcurrencyLimit":5}`,
		`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":5,"mode":"balanced_fast"}`,
		`{"defaultSoftConcurrency":0,"clientIpConcurrencyLimit":5}`,
		`{"defaultSoftConcurrency":1000001,"clientIpConcurrencyLimit":5}`,
		`{"clientIpConcurrencyLimit":-1,"defaultSoftConcurrency":5}`,
		`{"defaultSoftConcurrency":5.5,"clientIpConcurrencyLimit":5}`,
		`not json`,
		`[1]`,
	}
	for _, text := range invalid {
		if ValidateStoredTwoKeyPolicy(nullPolicy(text)) {
			t.Fatalf("%q 应判定完成态违规", text)
		}
	}
}
