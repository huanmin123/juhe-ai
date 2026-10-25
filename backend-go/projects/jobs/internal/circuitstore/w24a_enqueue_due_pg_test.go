// 波次 w24a：EnqueueDue 到期转移链真实 PG 回归（W24① 生产臂修复随批）。
// 缺陷背景：next_transition_at 在两种方言里都是 text 列（RFC3339Nano 文本），
// 到期比较靠双方言一致的字典序；此前 instantParam 把时间参数解析成原生
// time.Time 绑定，pgx 对 text 列参数以 Go 默认文本形态（含空格与 " +0000 UTC"
// 后缀）参与字典序比较，与行内 RFC3339Nano 恒不匹配——到期候选扫描恒 0 行，
// PG 模式到期转移静默失效（2026-10-25 生产 log_transaction_sample_rate 采样
// 实证：EnqueueDue 恒空候选恒回滚）。修复=直接绑定 RFC3339Nano 文本；本测试
// 钉住「到期行必须入队（非 0）、未到期行不得入队」。原组B三个 SQLite 取证
// 测试（TestEnqueueDueTransitionsDueProjections 等）已在后续切片删除，悬空
// 引用随本批以本测试替代。
package circuitstore

import (
	"context"
	"testing"
	"time"
)

func TestW24AEnqueueDueReturnsDueRowsOnPostgres(t *testing.T) {
	repo, db := openListAvailabilityFixture(t)
	ctx := context.Background()

	now := time.Now().UTC()
	nowMS := now.UnixMilli()
	due := now.Add(-time.Minute).Format(time.RFC3339Nano)
	future := now.Add(time.Hour).Format(time.RFC3339Nano)

	seedProjection := func(accountID, nameSortKey, nextTransitionAt string) {
		t.Helper()
		if _, err := db.Exec(`
      INSERT INTO juhe_business.account_list_availability_projections (
        viewer_system_account_id, account_id, effective_status, schedulable_bucket,
        account_type, name_sort_key, priority_sort_key, super_priority_sort_key,
        fallback_sort_key, concurrency_sort_key, created_at_sort_key,
        payload_json, source_generation, next_transition_at, projected_at
      ) VALUES ('w24a-viewer', $1, 'active', 'normal', 'api_key', $2, 0, 0, 0, 0,
                '2026-01-01T00:00:00.000Z', '{}', 1, $3, '2026-01-01T00:00:00.000Z')`,
			accountID, nameSortKey, nextTransitionAt); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := db.Exec(`INSERT INTO juhe_business.system_accounts (id) VALUES ('w24a-viewer')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.accounts (id, system_account_id) VALUES ('w24a-acc-due', 'w24a-viewer'), ('w24a-acc-future', 'w24a-viewer')`); err != nil {
		t.Fatal(err)
	}
	seedProjection("w24a-acc-due", "w24a-due", due)
	seedProjection("w24a-acc-future", "w24a-future", future)

	enqueued, err := repo.EnqueueDue(ctx, 10, nowMS)
	if err != nil {
		t.Fatal(err)
	}
	if enqueued != 1 {
		t.Fatalf("到期转移必须恰好入队 1 个到期账户（实际 %d；未到期行不得入队）", enqueued)
	}
	var reason string
	if err := db.QueryRow(`SELECT reason FROM juhe_business.account_list_availability_dirty WHERE account_id = 'w24a-acc-due'`).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "projection_due_transition" {
		t.Fatalf("脏标记 reason 不符: %s", reason)
	}
	var futureDirty int
	if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_business.account_list_availability_dirty WHERE account_id = 'w24a-acc-future'`).Scan(&futureDirty); err != nil {
		t.Fatal(err)
	}
	if futureDirty != 0 {
		t.Fatal("未到期账户不得入队")
	}
}
