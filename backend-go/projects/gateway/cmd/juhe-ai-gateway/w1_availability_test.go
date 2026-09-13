package main

// w1: chain_accounts.go 可用性门（physical/resource）与质量排序直测。

import (
	"database/sql"
	"testing"
)

func TestW1ChainPhysicalAccountAvailable(t *testing.T) {
	now := int64(1_800_000_000_000)
	healthy := &chainCandidateRow{ID: "a", Status: "active", Schedulable: 1}
	ok, err := chainPhysicalAccountAvailable(healthy, now, false)
	if err != nil || !ok {
		t.Fatalf("healthy = %v, %v", ok, err)
	}
	// 过期账户。
	expired := &chainCandidateRow{ID: "b", Status: "active", Schedulable: 1,
		AccountExpiresAt: sql.NullString{String: "2020-01-01T00:00:00.000Z", Valid: true}}
	if ok, _ := chainPhysicalAccountAvailable(expired, now, false); ok {
		t.Fatal("过期账户必须不可用")
	}
	// 非法 expires 格式：错误。
	badTime := &chainCandidateRow{ID: "c", Status: "active", Schedulable: 1,
		AccountExpiresAt: sql.NullString{String: "not-a-time", Valid: true}}
	if _, err := chainPhysicalAccountAvailable(badTime, now, false); err == nil {
		t.Fatal("非法 expires 必须报错")
	}
	// 不可调度。
	unschedulable := &chainCandidateRow{ID: "d", Status: "active", Schedulable: 0}
	if ok, _ := chainPhysicalAccountAvailable(unschedulable, now, false); ok {
		t.Fatal("不可调度必须 false")
	}
	// includeUnavailable：rate_limited/temporary_unavailable 可见，cooldown 不生效。
	for _, status := range []string{"active", "rate_limited", "temporary_unavailable"} {
		row := &chainCandidateRow{ID: "e", Status: status, Schedulable: 1}
		if ok, _ := chainPhysicalAccountAvailable(row, now, true); !ok {
			t.Fatalf("includeUnavailable 下 %s 必须可见", status)
		}
	}
	if ok, _ := chainPhysicalAccountAvailable(&chainCandidateRow{ID: "f", Status: "disabled", Schedulable: 1}, now, true); ok {
		t.Fatal("disabled 即使 includeUnavailable 也不可见")
	}
	// 冷却中（严格模式）。
	cooldown := &chainCandidateRow{ID: "g", Status: "active", Schedulable: 1,
		CooldownUntil: sql.NullString{String: "2099-01-01T00:00:00.000Z", Valid: true}}
	if ok, _ := chainPhysicalAccountAvailable(cooldown, now, false); ok {
		t.Fatal("冷却中必须不可用")
	}
	// 冷却已过。
	cooldownPast := &chainCandidateRow{ID: "h", Status: "active", Schedulable: 1,
		CooldownUntil: sql.NullString{String: "2020-01-01T00:00:00.000Z", Valid: true}}
	if ok, _ := chainPhysicalAccountAvailable(cooldownPast, now, false); !ok {
		t.Fatal("冷却已过必须可用")
	}
	// 非法 cooldown 格式：错误。
	badCooldown := &chainCandidateRow{ID: "i", Status: "active", Schedulable: 1,
		CooldownUntil: sql.NullString{String: "junk", Valid: true}}
	if _, err := chainPhysicalAccountAvailable(badCooldown, now, false); err == nil {
		t.Fatal("非法 cooldown 必须报错")
	}
	// 非 active 严格模式。
	if ok, _ := chainPhysicalAccountAvailable(&chainCandidateRow{ID: "j", Status: "rate_limited", Schedulable: 1}, now, false); ok {
		t.Fatal("rate_limited 严格模式不可用")
	}
}

func TestW1ChainResourceAccountAvailable(t *testing.T) {
	now := int64(1_800_000_000_000)
	// 无授权实例：直接可用。
	if ok, err := chainResourceAccountAvailable(&chainCandidateRow{ID: "a"}, now, false); err != nil || !ok {
		t.Fatalf("无实例 = %v, %v", ok, err)
	}
	base := func() *chainCandidateRow {
		row := &chainCandidateRow{ID: "local"}
		row.AuthorizationInstanceAuthorizationID = sql.NullString{String: "authz_1", Valid: true}
		row.ResourceAccountID = sql.NullString{String: "res_1", Valid: true}
		row.ResourceStatus = sql.NullString{String: "active", Valid: true}
		row.ResourceSchedulable = sql.NullInt64{Int64: 1, Valid: true}
		return row
	}
	// 完整健康资源实例。
	if ok, err := chainResourceAccountAvailable(base(), now, false); err != nil || !ok {
		t.Fatalf("健康实例 = %v, %v", ok, err)
	}
	// 缺 resourceID / status：false。
	partial := base()
	partial.ResourceAccountID = sql.NullString{}
	if ok, _ := chainResourceAccountAvailable(partial, now, false); ok {
		t.Fatal("缺 resourceID 必须 false")
	}
	// 过期资源账户。
	expiredRes := base()
	expiredRes.ResourceAccountExpiresAt = sql.NullString{String: "2020-01-01T00:00:00.000Z", Valid: true}
	if ok, _ := chainResourceAccountAvailable(expiredRes, now, false); ok {
		t.Fatal("资源过期必须 false")
	}
	// account_expired 错误码。
	errored := base()
	errored.ResourceLastErrorCode = sql.NullString{String: "account_expired", Valid: true}
	if ok, _ := chainResourceAccountAvailable(errored, now, false); ok {
		t.Fatal("account_expired 必须 false")
	}
	// 不可调度资源。
	unschedulableRes := base()
	unschedulableRes.ResourceSchedulable = sql.NullInt64{Int64: 0, Valid: true}
	if ok, _ := chainResourceAccountAvailable(unschedulableRes, now, false); ok {
		t.Fatal("资源不可调度必须 false")
	}
	// includeUnavailable 三态。
	for _, status := range []string{"active", "rate_limited", "temporary_unavailable"} {
		row := base()
		row.ResourceStatus = sql.NullString{String: status, Valid: true}
		if ok, _ := chainResourceAccountAvailable(row, now, true); !ok {
			t.Fatalf("includeUnavailable 下 %s 必须可见", status)
		}
	}
	// 冷却。
	cooldownRes := base()
	cooldownRes.ResourceCooldownUntil = sql.NullString{String: "2099-01-01T00:00:00.000Z", Valid: true}
	if ok, _ := chainResourceAccountAvailable(cooldownRes, now, false); ok {
		t.Fatal("资源冷却中必须 false")
	}
	pastRes := base()
	pastRes.ResourceCooldownUntil = sql.NullString{String: "2020-01-01T00:00:00.000Z", Valid: true}
	if ok, _ := chainResourceAccountAvailable(pastRes, now, false); !ok {
		t.Fatal("资源冷却已过必须可用")
	}
	// 非法时间格式。
	badRes := base()
	badRes.ResourceAccountExpiresAt = sql.NullString{String: "junk", Valid: true}
	if _, err := chainResourceAccountAvailable(badRes, now, false); err == nil {
		t.Fatal("非法资源时间必须报错")
	}
	badCooldownRes := base()
	badCooldownRes.ResourceCooldownUntil = sql.NullString{String: "junk", Valid: true}
	if _, err := chainResourceAccountAvailable(badCooldownRes, now, false); err == nil {
		t.Fatal("非法资源冷却必须报错")
	}
}

func TestW1CompareChainQuality(t *testing.T) {
	collator := chainNameCollator()
	score := 90.0
	lowScore := 50.0
	// 有分数优先于无分数。
	left := &chainCandidateRow{ID: "a", Name: "甲", QualityScore: &score}
	right := &chainCandidateRow{ID: "b", Name: "乙"}
	if compareChainQuality(left, right, collator) != -1 {
		t.Fatal("有分数必须在前")
	}
	if compareChainQuality(right, left, collator) != 1 {
		t.Fatal("无分数必须在后")
	}
	// 分数升序。
	if compareChainQuality(left, &chainCandidateRow{ID: "c", Name: "甲", QualityScore: &lowScore}, collator) != 1 {
		t.Fatal("高分必须排后（升序）")
	}
	// 同分按名。
	if compareChainQuality(left, left, collator) != 0 {
		t.Fatal("同值必须 0")
	}
	// boolToInt。
	if boolToInt(true) != 1 || boolToInt(false) != 0 {
		t.Fatal("boolToInt 错误")
	}
}
