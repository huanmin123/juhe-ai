package gatewayusage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Usage record spool mirroring backend/src/modules/gateway/usage/
// usage-record-spool.ts: the performance-mode disk compensation that keeps
// usage records when the Redis Stream enqueue fails, replaying them through
// the enqueue port once it recovers.

// Spool capacity and retention constants (usage-record-spool.ts).
const (
	spoolCapacityRefreshIntervalMs = 30_000
	spoolTemporaryFileRetentionMs  = 60 * 60_000
)

// UsageRecordSpoolRuntime mirrors UsageRecordSpoolRuntime.
type UsageRecordSpoolRuntime struct {
	PendingItems       int
	PendingBytes       int
	PersistedCount     int
	ReplayedCount      int
	PersistFailureCount int
	ReplayFailureCount int
	LastPersistedAt    string
	LastReplayedAt     string
	LastError          string
}

// SpoolReplay ports the replay consumer: enqueue one spooled record back
// into the write pipeline (Node startUsageRecordSpoolReplay argument).
type SpoolReplay interface {
	Replay(ctx Ctx, input UsageRecordInput) error
}

// SpoolConfig mirrors the runtimeConfig.usageSpool facts.
type SpoolConfig struct {
	Directory        string
	InstanceID       string
	MaxItems         int
	MaxBytes         int
	ReplayBatchSize  int
	ReplayIntervalMs int
	// Enabled mirrors runtimeConfig.runtimeMode === 'performance'.
	Enabled bool
}

// UsageRecordSpool is the file-backed compensation store. All operations are
// serialized like the Node persistSequence promise chain.
type UsageRecordSpool struct {
	config SpoolConfig
	clock  Clock
	logger Logger

	mu      sync.Mutex
	runtime UsageRecordSpoolRuntime
	capacity *spoolCapacity

	replayMu     sync.Mutex
	replayStop   bool
	replayWake   chan struct{}
	replayActive bool
}

type spoolCapacity struct {
	items       int
	bytes       int
	refreshedAt time.Time
}

// NewUsageRecordSpool builds the spool.
func NewUsageRecordSpool(config SpoolConfig, clock Clock, logger Logger) *UsageRecordSpool {
	if clock == nil {
		clock = SystemClock{}
	}
	return &UsageRecordSpool{
		config: config,
		clock:  clock,
		logger: logger,
		replayWake: make(chan struct{}, 1),
	}
}

// Runtime mirrors getUsageRecordSpoolRuntime.
func (s *UsageRecordSpool) Runtime() UsageRecordSpoolRuntime {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtime
}

// Persist mirrors persistUsageRecordToSpool: atomic write of
// `${JSON(input)}\n` under the instance directory with a capacity guard.
// Callers on the J-F/G20 write path normalize the record first, so the
// persisted document carries a stable id/createdAt (parseUsageRecord
// rejects spool files without them, exactly like the Node replay).
func (s *UsageRecordSpool) Persist(ctx Ctx, input UsageRecordInput) error {
	if !s.config.Enabled {
		return errors.New("usage spool 只能在 performance 模式使用")
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return s.recordPersistFailure(err)
	}
	encoded = append(encoded, '\n')
	fail := func(err error) error {
		s.runtime.PersistFailureCount++
		s.runtime.LastError = err.Error()
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.currentCapacityLocked(false)
	if err != nil {
		return fail(err)
	}
	if current.items+1 > s.config.MaxItems || current.bytes+len(encoded) > s.config.MaxBytes {
		refreshed, err := s.scanCapacity()
		if err != nil {
			return fail(err)
		}
		if refreshed.items+1 > s.config.MaxItems || refreshed.bytes+len(encoded) > s.config.MaxBytes {
			return fail(errors.New("usage spool 已达到容量上限：items=" + itoa(refreshed.items) + ", bytes=" + itoa(refreshed.bytes)))
		}
		s.capacity = &spoolCapacity{items: refreshed.items, bytes: refreshed.bytes, refreshedAt: s.clock.Now()}
		current = s.capacity
	}
	directory := s.instanceDirectory()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fail(err)
	}
	token := itoa64(s.clock.Now().UnixMilli()) + "-" + itoa(os.Getpid()) + "-" + newUUID()
	temporaryPath := filepath.Join(directory, "."+token+".tmp")
	finalPath := filepath.Join(directory, token+".json")
	if err := writeFileSync(temporaryPath, encoded); err != nil {
		return fail(err)
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return fail(err)
	}
	if current == nil {
		current = &spoolCapacity{}
	}
	current.items++
	current.bytes += len(encoded)
	s.capacity = current
	s.runtime.PendingItems = current.items
	s.runtime.PendingBytes = current.bytes
	s.runtime.PersistedCount++
	s.runtime.LastPersistedAt = s.clock.Now().UTC().Format(timeRFC3339Millis)
	return nil
}

func (s *UsageRecordSpool) recordPersistFailure(err error) error {
	s.mu.Lock()
	s.runtime.PersistFailureCount++
	s.runtime.LastError = err.Error()
	s.mu.Unlock()
	return err
}

// currentCapacityLocked mirrors currentCapacity with the 30s cache.
func (s *UsageRecordSpool) currentCapacityLocked(force bool) (*spoolCapacity, error) {
	if !force && s.capacity != nil && time.Since(s.capacity.refreshedAt) < spoolCapacityRefreshIntervalMs {
		return s.capacity, nil
	}
	scan, err := s.scanCapacity()
	if err != nil {
		return nil, err
	}
	s.capacity = &spoolCapacity{items: scan.items, bytes: scan.bytes, refreshedAt: s.clock.Now()}
	s.runtime.PendingItems = s.capacity.items
	s.runtime.PendingBytes = s.capacity.bytes
	return s.capacity, nil
}

// scanCapacity mirrors scanSpoolCapacity: count every .json/.tmp/.corrupt
// file under the instance directories, pruning stale .tmp files.
func (s *UsageRecordSpool) scanCapacity() (spoolCapacity, error) {
	items := 0
	bytes := 0
	directories, err := s.listInstanceDirectories()
	if err != nil {
		return spoolCapacity{}, err
	}
	now := s.clock.Now()
	for _, directory := range directories {
		entries, err := os.ReadDir(directory)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return spoolCapacity{}, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".tmp") && !strings.HasSuffix(name, ".corrupt") {
				continue
			}
			fullPath := filepath.Join(directory, name)
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if strings.HasSuffix(name, ".tmp") && now.Sub(info.ModTime()) >= spoolTemporaryFileRetentionMs {
				if err := os.Remove(fullPath); err != nil && !errors.Is(err, os.ErrNotExist) {
					return spoolCapacity{}, err
				}
				continue
			}
			items++
			bytes += int(info.Size())
		}
	}
	return spoolCapacity{items: items, bytes: bytes}, nil
}

func (s *UsageRecordSpool) instanceDirectory() string {
	return filepath.Join(s.config.Directory, s.config.InstanceID)
}

func (s *UsageRecordSpool) listInstanceDirectories() ([]string, error) {
	entries, err := os.ReadDir(s.config.Directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var directories []string
	for _, entry := range entries {
		if entry.IsDir() {
			directories = append(directories, filepath.Join(s.config.Directory, entry.Name()))
		}
	}
	sort.Strings(directories)
	return directories, nil
}

// listSpoolFiles mirrors listSpoolFiles: round-robin directory cursor,
// .json files only, sorted, capped at the batch size.
func (s *UsageRecordSpool) listSpoolFiles(limit int) ([]string, error) {
	directories, err := s.listInstanceDirectories()
	if err != nil {
		return nil, err
	}
	if len(directories) == 0 {
		return nil, nil
	}
	var files []string
	for offset := 0; offset < len(directories) && len(files) < limit; offset++ {
		directory := directories[offset%len(directories)]
		entries, err := os.ReadDir(directory)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			files = append(files, filepath.Join(directory, entry.Name()))
			if len(files) >= limit {
				break
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

// parseUsageRecord mirrors parseUsageRecord: reject non-objects and records
// without a stable id/createdAt.
func parseUsageRecord(text string, filePath string) (UsageRecordInput, error) {
	var input UsageRecordInput
	if err := json.Unmarshal([]byte(text), &input); err != nil {
		return UsageRecordInput{}, errors.New("usage spool 文件格式错误：" + filepath.Base(filePath))
	}
	if input.ID == "" || input.CreatedAt == "" {
		return UsageRecordInput{}, errors.New("usage spool 文件缺少稳定 id/createdAt：" + filepath.Base(filePath))
	}
	return input, nil
}

// RunReplayOnce mirrors one iteration of runUsageRecordSpoolReplay: drain up
// to replayBatchSize files, quarantining corrupt files and keeping failed
// ones for the next round. Exposed so the J-F/G20 assembly can drive the
// loop on its own scheduler; StartReplay/StopReplay provide the built-in
// loop.
func (s *UsageRecordSpool) RunReplayOnce(ctx Ctx, replay SpoolReplay) (processed int, err error) {
	files, err := s.listSpoolFiles(s.config.ReplayBatchSize)
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, nil
	}
	for _, filePath := range files {
		content, err := os.ReadFile(filePath)
		if err != nil {
			s.recordReplayFailure(err)
			return processed, err
		}
		input, err := parseUsageRecord(string(content), filePath)
		if err != nil {
			s.recordReplayFailure(err)
			if quarantineErr := os.Rename(filePath, filePath+".corrupt"); quarantineErr != nil {
				return processed, quarantineErr
			}
			s.mu.Lock()
			s.capacity = nil
			s.mu.Unlock()
			continue
		}
		if err := replay.Replay(ctx, input); err != nil {
			s.recordReplayFailure(err)
			return processed, err
		}
		info, err := os.Stat(filePath)
		if err == nil {
			s.mu.Lock()
			s.runtime.PendingItems = maxInt(0, s.runtime.PendingItems-1)
			s.runtime.PendingBytes = maxInt(0, s.runtime.PendingBytes-int(info.Size()))
			s.mu.Unlock()
		}
		if err := os.Remove(filePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.recordReplayFailure(err)
			return processed, err
		}
		s.mu.Lock()
		s.runtime.ReplayedCount++
		s.runtime.LastReplayedAt = s.clock.Now().UTC().Format(timeRFC3339Millis)
		s.capacity = nil
		s.mu.Unlock()
		processed++
	}
	return processed, nil
}

func (s *UsageRecordSpool) recordReplayFailure(err error) {
	s.mu.Lock()
	s.runtime.ReplayFailureCount++
	s.runtime.LastError = err.Error()
	s.mu.Unlock()
}

// StartReplay mirrors startUsageRecordSpoolReplay: background replay loop
// with backoff on failure.
func (s *UsageRecordSpool) StartReplay(replay SpoolReplay) {
	if !s.config.Enabled {
		return
	}
	s.replayMu.Lock()
	if s.replayActive {
		s.replayMu.Unlock()
		return
	}
	s.replayActive = true
	s.replayStop = false
	s.replayMu.Unlock()
	go func() {
		for {
			s.replayMu.Lock()
			stopped := s.replayStop
			s.replayMu.Unlock()
			if stopped {
				return
			}
			processed, err := s.RunReplayOnce(context.Background(), replay)
			if err != nil {
				s.sleepReplayDelay(s.config.ReplayIntervalMs)
				continue
			}
			if processed == 0 {
				s.sleepReplayDelay(s.config.ReplayIntervalMs)
			}
		}
	}()
}

// StopReplay mirrors stopUsageRecordSpoolReplay.
func (s *UsageRecordSpool) StopReplay() {
	s.replayMu.Lock()
	s.replayStop = true
	wake := s.replayWake
	s.replayMu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (s *UsageRecordSpool) sleepReplayDelay(ms int) {
	if ms <= 0 {
		ms = 1
	}
	select {
	case <-s.replayWake:
	case <-time.After(time.Duration(ms) * time.Millisecond):
	}
}

// writeFileSync writes with exclusive creation and fsync, mirroring the
// Node open('wx') + writeFile + sync sequence.
func writeFileSync(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
