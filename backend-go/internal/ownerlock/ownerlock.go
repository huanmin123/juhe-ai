package ownerlock

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Metadata is diagnostic information written only after the OS lock is held.
// The lock state itself is owned by the file handle, not by this JSON payload.
type Metadata struct {
	DeploymentEpoch string `json:"deploymentEpoch"`
	RouteOwner      string `json:"routeOwner"`
	Version         string `json:"version"`
	PID             int    `json:"pid"`
	StartedAt       string `json:"startedAt"`
}

type Lock struct {
	file     *os.File
	path     string
	once     sync.Once
	unlockFn func(*os.File) error
}

func Acquire(path string, metadata Metadata) (*Lock, error) {
	if path == "" {
		return nil, fmt.Errorf("owner lock path cannot be empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create owner lock directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open owner lock %q: %w", path, err)
	}
	unlockFn, err := lockFile(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("owner lock %q is already held or unavailable: %w", path, err)
	}

	metadata.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	encoded, err := json.Marshal(metadata)
	if err != nil {
		_ = unlockFn(file)
		_ = file.Close()
		return nil, fmt.Errorf("encode owner lock metadata: %w", err)
	}
	if err := writeMetadata(file, encoded); err != nil {
		_ = unlockFn(file)
		_ = file.Close()
		return nil, fmt.Errorf("write owner lock metadata: %w", err)
	}
	return &Lock{file: file, path: path, unlockFn: unlockFn}, nil
}

func (lock *Lock) Release() error {
	if lock == nil {
		return nil
	}
	var releaseErr error
	lock.once.Do(func() {
		if err := lock.unlockFn(lock.file); err != nil {
			releaseErr = fmt.Errorf("release owner lock %q: %w", lock.path, err)
		}
		if err := lock.file.Close(); err != nil && releaseErr == nil {
			releaseErr = fmt.Errorf("close owner lock %q: %w", lock.path, err)
		}
	})
	return releaseErr
}

func writeMetadata(file *os.File, encoded []byte) error {
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	_, err := file.Write(encoded)
	return err
}
