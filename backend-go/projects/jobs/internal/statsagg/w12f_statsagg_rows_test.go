package statsagg

// w12f_statsagg_rows_test.go 直调 ShouldAggregateUsageStatsRecord 的校验
// 矩阵与 UsageStatsEntries 的维度扇出，覆盖凭据/授权字段的防御分支组合。

import (
	"testing"
)

func w12fStr(v string) *string { return &v }

func TestW12fShouldAggregateRejects(t *testing.T) {
	base := UsageStatsRecordRow{SystemAccountID: "w12f", CreatedAt: "2026-09-14T10:00:00.000Z"}
	baseAccount := func() UsageStatsRecordRow {
		row := base
		row.AccountID = w12fStr("acc-1")
		row.AccountOwnerSystemAccountID = w12fStr("owner")
		row.AccountAccessType = w12fStr("owner")
		return row
	}
	rejects := []struct {
		name string
		row  UsageStatsRecordRow
	}{
		{"access type 非法", func() UsageStatsRecordRow {
			row := baseAccount()
			row.AccountAccessType = w12fStr("superuser")
			return row
		}()},
		{"group access type 非法", func() UsageStatsRecordRow {
			row := baseAccount()
			row.GroupID = w12fStr("grp")
			row.GroupOwnerSystemAccountID = w12fStr("owner")
			row.GroupAccessType = w12fStr("superuser")
			return row
		}()},
		{"account source type 非法", func() UsageStatsRecordRow {
			row := baseAccount()
			row.AccountAuthorizationSourceType = w12fStr("alien")
			return row
		}()},
		{"group source type 非法", func() UsageStatsRecordRow {
			row := baseAccount()
			row.GroupID = w12fStr("grp")
			row.GroupOwnerSystemAccountID = w12fStr("owner")
			row.GroupAccessType = w12fStr("owner")
			row.GroupAuthorizationSourceType = w12fStr("alien")
			return row
		}()},
		{"无 account 但带 owner", func() UsageStatsRecordRow {
			row := base
			row.AccountOwnerSystemAccountID = w12fStr("owner")
			return row
		}()},
		{"无 group 但带 group owner", func() UsageStatsRecordRow {
			row := base
			row.GroupOwnerSystemAccountID = w12fStr("owner")
			return row
		}()},
		{"account_authorized 缺授权", func() UsageStatsRecordRow {
			row := baseAccount()
			row.AccountAccessType = w12fStr("account_authorized")
			return row
		}()},
		{"group_authorized 缺组", func() UsageStatsRecordRow {
			row := baseAccount()
			row.AccountAccessType = w12fStr("group_authorized")
			return row
		}()},
		{"owner 带授权字段", func() UsageStatsRecordRow {
			row := baseAccount()
			row.AccountAuthorizationID = w12fStr("auth-1")
			return row
		}()},
		{"group owner 带授权字段", func() UsageStatsRecordRow {
			row := baseAccount()
			row.GroupID = w12fStr("grp")
			row.GroupOwnerSystemAccountID = w12fStr("owner")
			row.GroupAccessType = w12fStr("owner")
			row.GroupAuthorizationID = w12fStr("gauth")
			return row
		}()},
		{"无授权 ID 带来源类型", func() UsageStatsRecordRow {
			row := baseAccount()
			row.AccountAuthorizationSourceType = w12fStr("manual")
			return row
		}()},
		{"无组授权 ID 带组来源", func() UsageStatsRecordRow {
			row := baseAccount()
			row.GroupAuthorizationSourceType = w12fStr("manual")
			return row
		}()},
	}
	for _, tc := range rejects {
		if ShouldAggregateUsageStatsRecord(tc.row) {
			t.Fatalf("%s 必须拒绝聚合", tc.name)
		}
	}
	// 合法 owner 行必须通过。
	if !ShouldAggregateUsageStatsRecord(baseAccount()) {
		t.Fatal("合法 owner 行必须通过")
	}
}

func TestW12fUsageStatsEntriesMetadataGuards(t *testing.T) {
	// account_id 为 nil → 无 account 元数据 → 只有 system_account/global 维度。
	row := UsageStatsRecordRow{SystemAccountID: "w12f-sys", CreatedAt: "2026-09-14T10:00:00.000Z"}
	entries := UsageStatsEntries(row, nil)
	if len(entries) < 1 || entries[0].ScopeType != "system_account" {
		t.Fatalf("基础维度缺失: %+v", entries)
	}
}
