package gatewayusage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestSpool(t *testing.T, enabled bool) (*UsageRecordSpool, string) {
	t.Helper()
	directory := t.TempDir()
	spool := NewUsageRecordSpool(SpoolConfig{
		Directory:        directory,
		InstanceID:       "inst-1",
		MaxItems:         10,
		MaxBytes:         64 * 1024,
		ReplayBatchSize:  8,
		ReplayIntervalMs: 5,
		Enabled:          enabled,
	}, fixedClock{ms: 1700000000000}, nil)
	return spool, directory
}

// waitForSpoolSettled waits until the spool has persisted exactly want
// records. PersistedCount/PendingItems are updated only after the final
// rename — Persist's last filesystem operation — so once the condition holds
// no spool writer can still create, rename or remove files under the
// directory. Tests that hand a spool directory to t.TempDir must settle it
// before returning, or the deferred RemoveAll can race a background writer
// and fail with "directory is not empty" under load.
func waitForSpoolSettled(t *testing.T, spool *UsageRecordSpool, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		runtime := spool.Runtime()
		if runtime.PersistedCount == want && runtime.PendingItems == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("spool writer 未结算：runtime %+v", runtime)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestSpoolPersistDisabledFails(t *testing.T) {
	spool, _ := newTestSpool(t, false)
	err := spool.Persist(context.Background(), UsageRecordInput{TraceID: "t", TrafficSource: "gateway", Success: true})
	if err == nil || err.Error() != "usage spool 只能在 performance 模式使用" {
		t.Fatalf("err = %v", err)
	}
}

func TestSpoolPersistAndReplay(t *testing.T) {
	spool, directory := newTestSpool(t, true)
	input := UsageRecordInput{
		ID:            "usage_20231114_s1_test",
		TraceID:       "trace-spool-1",
		TrafficSource: TrafficSourceGateway,
		Success:       true,
		SystemAccountID: "sys-owner",
		Model:         "gpt-requested",
		CreatedAt:     "2023-11-14T22:13:20.123Z",
	}
	if err := spool.Persist(context.Background(), input); err != nil {
		t.Fatalf("persist = %v", err)
	}
	instanceDirectory := filepath.Join(directory, "inst-1")
	entries, err := os.ReadDir(instanceDirectory)
	if err != nil {
		t.Fatalf("readdir = %v", err)
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".json") {
		t.Fatalf("spool files = %v", entries)
	}
	runtime := spool.Runtime()
	if runtime.PendingItems != 1 || runtime.PersistedCount != 1 || runtime.LastPersistedAt == "" {
		t.Fatalf("runtime = %+v", runtime)
	}

	// Replay consumes the file through the port and removes it.
	replay := &recordingReplay{}
	if _, err := spool.RunReplayOnce(context.Background(), replay); err != nil {
		t.Fatalf("replay = %v", err)
	}
	if len(replay.inputs) != 1 || replay.inputs[0].TraceID != "trace-spool-1" {
		t.Fatalf("replayed = %+v", replay.inputs)
	}
	if replay.inputs[0].CreatedAt != input.CreatedAt || replay.inputs[0].Model != input.Model {
		t.Fatalf("replayed record mismatch: %+v", replay.inputs[0])
	}
	entries, err = os.ReadDir(instanceDirectory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("spool not drained: %v %v", entries, err)
	}
	runtime = spool.Runtime()
	if runtime.ReplayedCount != 1 || runtime.LastReplayedAt == "" {
		t.Fatalf("runtime after replay = %+v", runtime)
	}
}

type recordingReplay struct {
	mu     sync.Mutex
	inputs []UsageRecordInput
	err    error
}

func (r *recordingReplay) Replay(ctx Ctx, input UsageRecordInput) error {
	if r.err != nil {
		return r.err
	}
	r.mu.Lock()
	r.inputs = append(r.inputs, input)
	r.mu.Unlock()
	return nil
}

func (r *recordingReplay) recorded() []UsageRecordInput {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]UsageRecordInput, len(r.inputs))
	copy(out, r.inputs)
	return out
}

func TestSpoolCorruptFileQuarantined(t *testing.T) {
	spool, directory := newTestSpool(t, true)
	instanceDirectory := filepath.Join(directory, "inst-1")
	if err := os.MkdirAll(instanceDirectory, 0o700); err != nil {
		t.Fatalf("mkdir = %v", err)
	}
	corruptPath := filepath.Join(instanceDirectory, "0001-broken.json")
	if err := os.WriteFile(corruptPath, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write = %v", err)
	}
	replay := &recordingReplay{}
	if _, err := spool.RunReplayOnce(context.Background(), replay); err != nil {
		t.Fatalf("replay = %v", err)
	}
	if len(replay.inputs) != 0 {
		t.Fatalf("replayed = %+v", replay.inputs)
	}
	if _, err := os.Stat(corruptPath + ".corrupt"); err != nil {
		t.Fatalf("corrupt quarantine missing: %v", err)
	}
	runtime := spool.Runtime()
	if runtime.ReplayFailureCount != 1 || runtime.LastError == "" {
		t.Fatalf("runtime = %+v", runtime)
	}
}

func TestSpoolReplayKeepsFileOnFailure(t *testing.T) {
	spool, directory := newTestSpool(t, true)
	input := UsageRecordInput{
		ID:            "usage_20231114_s2_test",
		TraceID:       "trace-spool-2",
		TrafficSource: TrafficSourceGateway,
		Success:       true,
		CreatedAt:     "2023-11-14T22:13:20.123Z",
	}
	if err := spool.Persist(context.Background(), input); err != nil {
		t.Fatalf("persist = %v", err)
	}
	replay := &recordingReplay{err: errReplayDown}
	if _, err := spool.RunReplayOnce(context.Background(), replay); err == nil {
		t.Fatal("replay failure must surface")
	}
	instanceDirectory := filepath.Join(directory, "inst-1")
	entries, _ := os.ReadDir(instanceDirectory)
	if len(entries) != 1 {
		t.Fatalf("failed replay must keep the spool file: %v", entries)
	}
	if spool.Runtime().ReplayFailureCount != 1 {
		t.Fatalf("runtime = %+v", spool.Runtime())
	}
}

var errReplayDown = errString("enqueue port down")

type errString string

func (e errString) Error() string { return string(e) }

func TestSpoolCapacityLimit(t *testing.T) {
	directory := t.TempDir()
	spool := NewUsageRecordSpool(SpoolConfig{
		Directory:  directory,
		InstanceID: "inst-1",
		MaxItems:   1,
		MaxBytes:   64 * 1024,
		Enabled:    true,
	}, fixedClock{ms: 1700000000000}, nil)
	input := UsageRecordInput{TraceID: "t", TrafficSource: "gateway", Success: true, CreatedAt: "2023-11-14T22:13:20.123Z"}
	if err := spool.Persist(context.Background(), input); err != nil {
		t.Fatalf("first persist = %v", err)
	}
	// Force a capacity rescan by expiring the cached window.
	spoolMu := spool
	spoolMu.mu.Lock()
	spoolMu.capacity = &spoolCapacity{items: 1, bytes: 10, refreshedAt: time.Now().Add(-time.Hour)}
	spoolMu.mu.Unlock()
	err := spool.Persist(context.Background(), input)
	if err == nil || !strings.Contains(err.Error(), "usage spool 已达到容量上限") {
		t.Fatalf("err = %v", err)
	}
}

func TestSpoolStartStopReplayLoop(t *testing.T) {
	spool, directory := newTestSpool(t, true)
	input := UsageRecordInput{
		ID:            "usage_20231114_s3_test",
		TraceID:       "trace-spool-3",
		TrafficSource: TrafficSourceGateway,
		Success:       true,
		CreatedAt:     "2023-11-14T22:13:20.123Z",
	}
	if err := spool.Persist(context.Background(), input); err != nil {
		t.Fatalf("persist = %v", err)
	}
	replay := &recordingReplay{}
	spool.StartReplay(replay)
	deadline := time.Now().Add(2 * time.Second)
	for len(replay.recorded()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	spool.StopReplay()
	if len(replay.recorded()) != 1 {
		t.Fatalf("loop replay = %+v", replay.recorded())
	}
	entries, _ := os.ReadDir(filepath.Join(directory, "inst-1"))
	if len(entries) != 0 {
		t.Fatalf("files left = %v", entries)
	}
}
