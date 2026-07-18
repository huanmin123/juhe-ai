package ownerlock

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquireRejectsSecondHolderAndReleasesWithoutDeletingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime", "owner.lock")
	first, err := Acquire(path, Metadata{DeploymentEpoch: "epoch-1", RouteOwner: "management", Version: "go-test", PID: os.Getpid()})
	if err != nil {
		t.Fatalf("Acquire first lock: %v", err)
	}
	defer first.Release()

	second, err := Acquire(path, Metadata{DeploymentEpoch: "epoch-2", RouteOwner: "management", Version: "go-test", PID: os.Getpid()})
	if err == nil || second != nil {
		t.Fatalf("Acquire second lock = %v, want an error", err)
	}
	if _, err := first.file.Seek(0, 0); err != nil {
		t.Fatalf("seek metadata: %v", err)
	}
	contents, err := io.ReadAll(first.file)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	var metadata Metadata
	if err := json.Unmarshal(contents, &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if metadata.DeploymentEpoch != "epoch-1" || metadata.RouteOwner != "management" || metadata.StartedAt == "" {
		t.Fatalf("metadata = %+v", metadata)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release first lock: %v", err)
	}
	third, err := Acquire(path, Metadata{DeploymentEpoch: "epoch-3", RouteOwner: "management", Version: "go-test", PID: os.Getpid()})
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	if err := third.Release(); err != nil {
		t.Fatalf("Release third lock: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file should remain for diagnostics: %v", err)
	}
}

func TestAcquireRejectsEmptyPath(t *testing.T) {
	_, err := Acquire("", Metadata{})
	if err == nil || !strings.Contains(err.Error(), "cannot be empty") {
		t.Fatalf("Acquire empty path error = %v", err)
	}
}
