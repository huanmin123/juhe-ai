package mockdata

// 分区确保链的 PG 集成测试：usage_records / chat_messages 日分区的日期键校验、
// 语句文本、真实建分区与幂等。语句契约与 jobs usagewriter / gateway chat
// partitions 对齐；测试库由 pgtestkit 提供，父表由 EnsurePostgres 建立。

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func TestPartitionDateKeyValidation(t *testing.T) {
	cases := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "2026-10-25T12:00:00.000Z", want: "20261025", ok: true},
		{input: "2026-10-25", want: "20261025", ok: true},
		{input: "2026-02-30T00:00:00Z", want: "", ok: false},
		{input: "20261301T00:00:00Z", want: "", ok: false},
		{input: "", want: "", ok: false},
		{input: "not-a-date", want: "", ok: false},
	}
	for _, item := range cases {
		got, ok := partitionDateKeyFromISO(item.input)
		if ok != item.ok || got != item.want {
			t.Fatalf("partitionDateKeyFromISO(%q) = %q,%v want %q,%v", item.input, got, ok, item.want, item.ok)
		}
	}
	// 归一化入口拒绝非 8 位与假日期。
	if _, ok := normalizePartitionDateKey("20261025"); !ok {
		t.Fatal("normalizePartitionDateKey 拒绝了合法日期键")
	}
	if _, ok := normalizePartitionDateKey("20261032"); ok {
		t.Fatal("normalizePartitionDateKey 接受了假日期")
	}
	if _, ok := normalizePartitionDateKey("2026-10-25"); ok {
		t.Fatal("normalizePartitionDateKey 接受了带连字符的键")
	}
}

func TestPartitionDDLText(t *testing.T) {
	usage, err := usageRecordPartitionDDL("20261025")
	if err != nil {
		t.Fatal(err)
	}
	wantUsage := "\n      CREATE TABLE IF NOT EXISTS juhe_usage.\"usage_records_20261025\"\n" +
		"      PARTITION OF juhe_usage.usage_records\n" +
		"      FOR VALUES FROM ('2026-10-25') TO ('2026-10-26')\n    "
	if usage != wantUsage {
		t.Fatalf("usage DDL = %q, want %q", usage, wantUsage)
	}
	chat, err := chatMessagePartitionDDL("20261025")
	if err != nil {
		t.Fatal(err)
	}
	wantChat := "\n      CREATE TABLE IF NOT EXISTS juhe_chat.\"chat_messages_20261025\"\n" +
		"      PARTITION OF juhe_chat.chat_messages\n" +
		"      FOR VALUES FROM ('2026-10-25') TO ('2026-10-26')\n    "
	if chat != wantChat {
		t.Fatalf("chat DDL = %q, want %q", chat, wantChat)
	}
	if _, err := usageRecordPartitionDDL("bad"); err == nil {
		t.Fatal("非法日期键应报错")
	}
}

// TestEnsurePartitionsCreatesDailyPartitions 在真实 PG 上验证：批内 created_at
// 生成对应日分区；同键重复执行幂等；chat 侧预建「当日 + 次日」；分区以
// relispartition=true 出现在 pg_class（覆盖枚举会排除它们）。
func TestEnsurePartitionsCreatesDailyPartitions(t *testing.T) {
	ctx := context.Background()
	db := pgtestkit.NewTestDatabase(t, func(ctx context.Context, target *sql.DB) error {
		_, err := schema.EnsurePostgres(ctx, target)
		return err
	}).DB

	memo := &partitionMemo{}
	createdAts := []string{
		"2026-10-23T10:00:00.000Z",
		"2026-10-24T00:30:00.000Z",
		"2026-10-23T22:00:00.000Z", // 与第一条同日：必须去重为一次 DDL。
	}
	if err := ensureUsageRecordPartitions(ctx, db, memo, createdAts); err != nil {
		t.Fatalf("ensureUsageRecordPartitions: %v", err)
	}
	if err := ensureChatMessagePartitions(ctx, db, memo, "2026-10-24T08:00:00.000Z"); err != nil {
		t.Fatalf("ensureChatMessagePartitions: %v", err)
	}
	// 重复执行（新备忘与旧备忘两种路径）必须幂等。
	if err := ensureUsageRecordPartitions(ctx, db, memo, createdAts); err != nil {
		t.Fatalf("重复 ensureUsageRecordPartitions: %v", err)
	}
	if err := ensureChatMessagePartitions(ctx, db, &partitionMemo{}, "2026-10-24T08:00:00.000Z"); err != nil {
		t.Fatalf("重复 ensureChatMessagePartitions: %v", err)
	}

	wantPartitions := map[string]bool{
		"usage_records_20261023": false,
		"usage_records_20261024": false,
		"chat_messages_20261024": false,
		"chat_messages_20261025": false, // chat 预建次日。
	}
	rows, err := db.QueryContext(ctx, `
		SELECT c.relname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname IN ('juhe_usage', 'juhe_chat')
		  AND c.relispartition = true AND c.relkind = 'r'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if _, want := wantPartitions[name]; !want {
			t.Fatalf("出现未预期的分区 %s", name)
		}
		wantPartitions[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for name, created := range wantPartitions {
		if !created {
			t.Fatalf("分区 %s 未创建", name)
		}
	}

	// schemaTables（覆盖与清理的枚举口径）必须排除分区子表、包含分区父表。
	usageTables, err := schemaTables(ctx, db, SchemaUsage)
	if err != nil {
		t.Fatal(err)
	}
	hasParent, hasChild := false, false
	for _, name := range usageTables {
		switch name {
		case "usage_records":
			hasParent = true
		case "usage_records_20261023":
			hasChild = true
		}
	}
	if !hasParent || hasChild {
		t.Fatalf("schemaTables(juhe_usage) 分区父子关系不符: parent=%v childSeen=%v tables=%v", hasParent, hasChild, usageTables)
	}
	// 无效 created_at 静默跳过（沿 jobs 写入路径语义），不 panic 不建分区。
	if err := ensureUsageRecordPartitions(ctx, db, &partitionMemo{}, []string{"garbage"}); err != nil {
		t.Fatalf("无效 created_at 应被跳过: %v", err)
	}
	// chat 侧无效时间显式报错（gateway 同语义）。
	if err := ensureChatMessagePartitions(ctx, db, &partitionMemo{}, "garbage"); err == nil {
		t.Fatal("无效 chat 时间应报错")
	}
}
