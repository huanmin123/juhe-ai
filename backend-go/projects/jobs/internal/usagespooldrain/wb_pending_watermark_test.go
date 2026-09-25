package usagespooldrain

// 统计链路排查修复 B① 的回归测试：drain 待删水位 OldestPendingCreatedAt
// 覆盖 spool 未确认文件段，供 ingestgate 游标安全门回退统计游标。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/usagewriter"
)

// spoolFileIn 按 gateway Persist 布局写单记录文件到指定实例目录。
func spoolFileIn(t *testing.T, directory string, instance string, token string, record usagewriter.UsageRecordInput) string {
	t.Helper()
	instanceDirectory := filepath.Join(directory, instance)
	if err := os.MkdirAll(instanceDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	return writeSpoolRaw(t, instanceDirectory, token, mustJSON(t, record))
}

func mustJSON(t *testing.T, record usagewriter.UsageRecordInput) string {
	t.Helper()
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded) + "\n"
}

func writeSpoolRaw(t *testing.T, instanceDirectory string, token string, content string) string {
	t.Helper()
	path := filepath.Join(instanceDirectory, token+".json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func baseRecordWithCreatedAt(id string, createdAt string) usagewriter.UsageRecordInput {
	record := baseRecord(id)
	record.CreatedAt = createdAt
	return record
}

func TestOldestPendingCreatedAtEmptyWithoutBacklog(t *testing.T) {
	enqueuer := &recordingEnqueuer{}
	missing := newTestDrainer(filepath.Join(t.TempDir(), "not-created"), enqueuer)
	if _, err := missing.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := missing.OldestPendingCreatedAt(); got != "" {
		t.Fatalf("无积压水位必须为空: %q", got)
	}
	empty := newTestDrainer(t.TempDir(), enqueuer)
	if _, err := empty.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := empty.OldestPendingCreatedAt(); got != "" {
		t.Fatalf("空目录水位必须为空: %q", got)
	}
}

func TestOldestPendingCreatedAtTracksHeadOnEnqueueFailure(t *testing.T) {
	directory := t.TempDir()
	drainer := newTestDrainer(directory, &failingEnqueuer{})
	spoolFileIn(t, directory, "gateway-chain", "0001-old", baseRecordWithCreatedAt("id-old", "2026-01-02T03:04:05.000Z"))
	spoolFileIn(t, directory, "gateway-chain", "0002-new", baseRecordWithCreatedAt("id-new", "2026-01-02T03:04:06.000Z"))

	if _, err := drainer.DrainOnce(context.Background()); err == nil {
		t.Fatal("入队全部失败必须报错")
	}
	if got := drainer.OldestPendingCreatedAt(); got != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("水位必须取队头记录 created_at: %q", got)
	}
}

func TestOldestPendingCreatedAtClearsAfterDrain(t *testing.T) {
	directory := t.TempDir()
	enqueuer := &recordingEnqueuer{}
	drainer := newTestDrainer(directory, enqueuer)
	spoolFileIn(t, directory, "gateway-chain", "0001-a", baseRecordWithCreatedAt("id-a", "2026-01-02T03:04:05.000Z"))

	if _, err := drainer.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := drainer.OldestPendingCreatedAt(); got != "" {
		t.Fatalf("积压清空后水位必须复位: %q", got)
	}
}

func TestOldestPendingCreatedAtCrossInstanceMinimum(t *testing.T) {
	directory := t.TempDir()
	drainer := newTestDrainer(directory, &failingEnqueuer{})
	// 两个实例目录：较旧记录在字典序更大的实例目录里，水位仍须取文件名
	// （持久化序）最旧的队头。
	spoolFileIn(t, directory, "instance-a", "0009-new", baseRecordWithCreatedAt("id-new", "2026-01-02T03:04:06.000Z"))
	spoolFileIn(t, directory, "instance-b", "0001-old", baseRecordWithCreatedAt("id-old", "2026-01-02T03:04:05.000Z"))

	if _, err := drainer.DrainOnce(context.Background()); err == nil {
		t.Fatal("入队全部失败必须报错")
	}
	if got := drainer.OldestPendingCreatedAt(); got != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("跨实例水位必须取文件名（持久化序）最旧的队头: %q", got)
	}
}

func TestOldestPendingCreatedAtQuarantinesCorruptHead(t *testing.T) {
	directory := t.TempDir()
	drainer := newTestDrainer(directory, &failingEnqueuer{})
	instanceDirectory := filepath.Join(directory, "gateway-chain")
	if err := os.MkdirAll(instanceDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	// 损坏队头 + 良好后继：本轮隔离损坏、良好文件入队失败保留；
	// 水位取良好文件的 created_at（损坏文件记录永不入表，不进游标面）。
	writeSpoolRaw(t, instanceDirectory, "0001-corrupt", "not-json\n")
	spoolFileIn(t, directory, "gateway-chain", "0002-good", baseRecordWithCreatedAt("id-good", "2026-01-02T03:04:07.000Z"))

	if _, err := drainer.DrainOnce(context.Background()); err == nil {
		t.Fatal("良好文件入队失败必须报错")
	}
	if _, err := os.Stat(filepath.Join(instanceDirectory, "0001-corrupt.json.corrupt")); err != nil {
		t.Fatalf("损坏队头必须被隔离: %v", err)
	}
	if got := drainer.OldestPendingCreatedAt(); got != "2026-01-02T03:04:07.000Z" {
		t.Fatalf("损坏队头隔离后水位必须取良好队头: %q", got)
	}
}

func TestOldestPendingCreatedAtShutdownDrainAlsoRefreshes(t *testing.T) {
	directory := t.TempDir()
	drainer := newTestDrainer(directory, &failingEnqueuer{})
	spoolFileIn(t, directory, "gateway-chain", "0001-a", baseRecordWithCreatedAt("id-a", "2026-01-02T03:04:05.000Z"))
	// DrainShutdown 走 DrainOnce 路径，同样刷新水位（失败保文件）。
	if processed := drainer.DrainShutdown(); processed != 0 {
		t.Fatalf("停机排空接受数 = %d, want 0", processed)
	}
	if got := drainer.OldestPendingCreatedAt(); got != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("停机排空后水位必须反映保留文件: %q", got)
	}
}
