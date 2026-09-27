package accounts

// 运行时并发 hydrate 回归（Node→Go 移植缺口：账户列表“并发数”恒 0/N）：
// ListPage 在响应组装处批量读网关进程内 tracker，填充 ListItem 的
// currentConcurrency（前端必填 number，缺失填 0；端口 nil 全 0；读数失败
// 降级 0 不阻断并 warn 留痕）。

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// fakeAccountConcurrencyReader records the requested account ids and serves a
// fixed concurrency map.
type fakeAccountConcurrencyReader struct {
	requested [][]string
	currents  map[string]int
	err       error
}

func (f *fakeAccountConcurrencyReader) LoadCurrentConcurrencyByID(_ context.Context, accountIDs []string) (map[string]int, error) {
	f.requested = append(f.requested, append([]string(nil), accountIDs...))
	if f.err != nil {
		return nil, f.err
	}
	return f.currents, nil
}

func concurrencyListItems(t *testing.T, env *testEnv) map[string]map[string]any {
	t.Helper()
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts", "")
	if code != http.StatusOK {
		t.Fatalf("列表应 200：%d %v", code, payload)
	}
	return listItems(t, payload)
}

// TestListPageHydratesCurrentConcurrency locks the batch overlay: every row
// carries its live counter, ids go to the reader in one batch, and missing
// accounts read as 0.
func TestListPageHydratesCurrentConcurrency(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-cc-1", adminID, "cc-1", "active")
	env.seedAccount(t, "acc-cc-2", adminID, "cc-2", "active")

	reader := &fakeAccountConcurrencyReader{currents: map[string]int{"acc-cc-1": 4}}
	env.store.SetConcurrencyReader(reader)

	items := concurrencyListItems(t, env)
	if got := items["acc-cc-1"]["currentConcurrency"]; got != float64(4) {
		t.Fatalf("acc-cc-1 并发应为 4：%v", items["acc-cc-1"])
	}
	// 部分账户缺失（tracker 未跟踪）→ 填 0，且键必须存在（前端 number 契约）。
	value, ok := items["acc-cc-2"]["currentConcurrency"]
	if !ok || value != float64(0) {
		t.Fatalf("acc-cc-2 并发缺失应渲染 0：%v (%v)", items["acc-cc-2"]["currentConcurrency"], ok)
	}
	if len(reader.requested) != 1 || len(reader.requested[0]) != 2 {
		t.Fatalf("应一次批量读 2 个账户 id：%v", reader.requested)
	}
}

// TestListPageCurrentConcurrencyNilReaderStaysZero keeps the degraded shape:
// without the port every row renders 0 and the list still succeeds.
func TestListPageCurrentConcurrencyNilReaderStaysZero(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-cc-nil", adminID, "cc-nil", "active")

	items := concurrencyListItems(t, env)
	value, ok := items["acc-cc-nil"]["currentConcurrency"]
	if !ok || value != float64(0) {
		t.Fatalf("端口 nil 时并发应渲染 0：%v (%v)", value, ok)
	}
}

// TestListPageCurrentConcurrencyReaderErrorDegradesToZero locks the
// degradation contract: a tracker failure never fails the page; rows fall
// back to 0.
func TestListPageCurrentConcurrencyReaderErrorDegradesToZero(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-cc-err", adminID, "cc-err", "active")
	env.store.SetConcurrencyReader(&fakeAccountConcurrencyReader{err: errors.New("tracker down")})

	items := concurrencyListItems(t, env)
	if got := items["acc-cc-err"]["currentConcurrency"]; got != float64(0) {
		t.Fatalf("读数失败应降级 0：%v", items["acc-cc-err"])
	}
}
