package proxylatency

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWFParseTargetURLRejects(t *testing.T) {
	for _, bad := range []string{
		"http://x.invalid/a%2Fb",                         // 路径携带编码斜杠
		"http://x.invalid/a/../b",                        // 点段
		"http://x.invalid/a\b",                           // 反斜杠
		"http://user:pw@x.invalid/v1",                    // 携带 userinfo
		"http://x.invalid/v1?query=1",                    // 携带查询
		"http://x.invalid/v1#fragment",                   // 携带片段
		"http://%x.invalid/v1",                           // host 携带百分号
		"http://x.invalid:0/v1",                          // 端口 0
		"https://" + strings.Repeat("a", 64) + ".com/v1", // label 超长
	} {
		if _, err := parseTargetURL(bad); err == nil {
			t.Fatalf("%q 必须拒绝", bad)
		}
	}
	if _, err := parseTargetURL("http://x.invalid" + strings.Repeat("/segment", 1)); err != nil {
		t.Fatal("合法路径不得拒绝")
	}
}

func TestWFRunCycleProjectionFailureRecorded(t *testing.T) {
	store := wfOpenJobsStore(t)
	business := wfOpenBusinessDB(t)
	projector := wfNewProjector(t, store, business)
	runner := NewRunner(RuntimeConfig{InstanceID: "wf-pfail", OwnerLease: time.Hour, ProxyLease: time.Hour}, store, &wfFakeReader{}, nil)
	runner.SetResultProjector(projector)
	ctx := context.Background()

	wfSeedProxyRow(t, business, "p-pfail", wfProjRevision, nil)
	owner, ok, err := store.AcquireOwnerLease(ctx, "wf-pfail", time.Hour)
	if err != nil || !ok {
		t.Fatalf("owner lease ok=%v err=%v", ok, err)
	}
	runner.reader = &wfFakeReader{drafts: []InputDraft{wfCycleDraft("p-pfail")}}
	runner.executeIssuedInput = func(_ context.Context, store *Store, owner OwnerLease, proxy ProxyLease, issued IssuedInput, _ ExecutorOptions) (Outcome, bool, error) {
		outcome, committed := wfCycleCommitOutcome(t, store, owner, proxy, issued)
		return outcome, committed, nil
	}
	if _, err := business.Exec(`DROP TABLE proxy_latency_projection_receipts`); err != nil {
		t.Fatalf("准备投影失败: %v", err)
	}
	if err := runner.runCycle(ctx, owner); err != nil {
		t.Fatalf("投影失败不得致命: %v", err)
	}
	status := runner.Status()
	if status.ExecutionFailures != 1 || !strings.Contains(status.LastError, "receipt") {
		t.Fatalf("状态=%+v", status)
	}
	_ = errors.New
}
