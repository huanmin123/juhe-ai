package statreads

// compactionExpected 列表映射断言（用量记录压缩标记设计 §压缩请求识别）：
// PG 原生 bool 与 SQLite integer 0/1 两种驱动解码形态都映射为非 omitempty
// 的 compactionExpected；升级期旧行缺列 / NULL 均按 false，不报错。

import "testing"

func TestUsageRecordListCompactionExpectedArms(t *testing.T) {
	deps := &Deps{}
	row := func(value any, present bool) Row {
		base := Row{
			"id": "ur-compaction", "trace_id": "trace-compaction",
			"traffic_source": "gateway", "success": true,
		}
		if present {
			base["compaction_expected"] = value
		}
		return base
	}
	assertExpected := func(name string, item usageRecordListItem, want bool) {
		t.Helper()
		if item.CompactionExpected != want {
			t.Fatalf("%s compactionExpected = %v, want %v", name, item.CompactionExpected, want)
		}
	}
	// PG 臂：pgx 把 boolean 列解码为 Go bool。
	assertExpected("PG bool true", deps.mapUsageRecordListItem(row(true, true), AccessScope{}, nil, nil, nil, nil), true)
	assertExpected("PG bool false", deps.mapUsageRecordListItem(row(false, true), AccessScope{}, nil, nil, nil, nil), false)
	// SQLite 臂：INTEGER 0/1 解码为 int64。
	assertExpected("SQLite int64(1)", deps.mapUsageRecordListItem(row(int64(1), true), AccessScope{}, nil, nil, nil, nil), true)
	assertExpected("SQLite int64(0)", deps.mapUsageRecordListItem(row(int64(0), true), AccessScope{}, nil, nil, nil, nil), false)
	// 旧行缺列与 NULL：boolLike 缺键/nil 都按 false。
	assertExpected("missing column", deps.mapUsageRecordListItem(row(nil, false), AccessScope{}, nil, nil, nil, nil), false)
	assertExpected("NULL column", deps.mapUsageRecordListItem(row(nil, true), AccessScope{}, nil, nil, nil, nil), false)
}
