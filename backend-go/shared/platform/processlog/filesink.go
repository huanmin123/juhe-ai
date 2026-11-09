package processlog

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// current 运行日志文件名是 jobs runtimelog 索引器的消费面契约
// (filename.go legacyRuntimeLogRoles: juhe-ai.log → server 角色)；改名会让
// 整个运行日志 grep 面失去 server 侧数据源。
const CurrentLogFileName = "juhe-ai.log"

// DefaultMaxFileBytes 对齐 Node 写侧契约的默认轮转阈值
// (archived shared/logger.ts RotatingLogStream maxFileBytes 默认 100MB)。
const DefaultMaxFileBytes int64 = 100 * 1024 * 1024

// FileSinkOptions 是 FileSink 的构造参数；零值即生产默认。
type FileSinkOptions struct {
	// FileName 是 current 日志文件名（须为不含路径的 .log 文件名）；空值取
	// CurrentLogFileName。多实例共享日志目录时由调用方传入
	// `juhe-ai.<instanceID>.log`。
	FileName string
	// MaxFileBytes 是大小轮转阈值；<=0 时取 DefaultMaxFileBytes。
	MaxFileBytes int64
	// MaxFiles / Retention 是本地轮转文件保留上限（2026-11-09 运行日志不再
	// 由 jobs F1 清理，改由写侧在轮转后自清理）；<=0 时分别取 DefaultMaxFiles
	// 与 DefaultRetention。
	MaxFiles  int
	Retention time.Duration
	// RetryInterval 是写失败/句柄失效后重开文件的节流间隔；<=0 时取
	// DefaultFileSinkRetryInterval（多机 NFS 断连恢复后无需重启进程即可自愈）。
	RetryInterval time.Duration
	// Now 与 NewToken 是轮转文件名的时间戳/token 注入点（测试用）。nil 时
	// 分别取 UTC time.Now 与 crypto/rand 派生的 8-4-4-4-12 小写 hex。
	Now      func() time.Time
	NewToken func() string
}

// DefaultFileSinkRetryInterval 是写失败后重开文件的默认节流间隔：避免持续
// 失败时每次写都触发 open 系统调用（NFS 黑洞场景下 open 可能阻塞），同时
// 保证共享目录恢复后在约 5s 内自动续写。
const DefaultFileSinkRetryInterval = 5 * time.Second

// 本地轮转文件保留默认值（与既有 JUHE_AI_LOG_MAX_FILES / RETENTION_DAYS 一致）。
const (
	DefaultMaxFiles  = 500
	DefaultRetention = 30 * 24 * time.Hour
)

// FileSink 是按大小滚动的 append-only JSONL 文件写侧。轮转判定与 Node
// RotatingLogStream._write 相同：currentSize > 0 且追加后超出阈值才轮转，
// 因此单条超阈记录仍完整写入而非拒写。写侧在每次轮转后按 MaxFiles/Retention
// 清理本实例的 rotated 文件（2026-11-09 起 jobs F1 索引与清理下架）。Write 保证
// 每条记录一次 write 落盘 (O_APPEND 单写原子性)，进程内不会出现交叉写撕裂行。
//
// 失败恢复：写入错误（EIO/ESTALE/句柄失效）或轮转重开失败后关闭并置空
// 句柄，按 retryInterval 节流重试 open；共享目录（NFS/云 NAS）恢复后无需
// 重启进程即可自愈。节流期内 Write 返回"不可用"错误，slog handler 丢弃该
// 条记录（不中断进程），恢复后后续记录正常落盘。
type FileSink struct {
	mu            sync.Mutex
	dir           string
	fileName      string
	maxFileBytes  int64
	maxFiles      int
	retention     time.Duration
	retryInterval time.Duration
	now           func() time.Time
	newToken      func() string
	openFile      func(name string, flag int, perm os.FileMode) (*os.File, error)
	currentPath   string
	file          *os.File
	currentSize   int64
	nextReopenAt  time.Time
	closed        bool
}

// NewFileSink 创建（必要时）并打开 dir 下的 current 日志文件，回读现有
// 大小作为轮转基线（进程重启后从真实文件大小继续判定阈值）。目录创建或
// 文件打开失败返回 error，由调用方决定降级路径。
func NewFileSink(dir string, options FileSinkOptions) (*FileSink, error) {
	fileName := strings.TrimSpace(options.FileName)
	if fileName == "" {
		fileName = CurrentLogFileName
	}
	if fileName != filepath.Base(fileName) || !strings.HasSuffix(fileName, ".log") {
		return nil, fmt.Errorf("运行日志文件名无效（必须是单段 .log 文件名）: %q", options.FileName)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建运行日志目录 %s 失败: %w", dir, err)
	}
	sink := &FileSink{
		dir:           dir,
		fileName:      fileName,
		maxFileBytes:  options.MaxFileBytes,
		maxFiles:      options.MaxFiles,
		retention:     options.Retention,
		retryInterval: options.RetryInterval,
		now:           options.Now,
		newToken:      options.NewToken,
		openFile:      os.OpenFile,
	}
	if sink.maxFileBytes <= 0 {
		sink.maxFileBytes = DefaultMaxFileBytes
	}
	if sink.maxFiles <= 0 {
		sink.maxFiles = DefaultMaxFiles
	}
	if sink.retention <= 0 {
		sink.retention = DefaultRetention
	}
	if sink.retryInterval <= 0 {
		sink.retryInterval = DefaultFileSinkRetryInterval
	}
	if sink.now == nil {
		sink.now = func() time.Time { return time.Now().UTC() }
	}
	if sink.newToken == nil {
		sink.newToken = newRandomToken
	}
	if err := sink.openCurrentLocked(); err != nil {
		return nil, err
	}
	sink.pruneLocked()
	return sink, nil
}

// Write 追加一条记录；写前按阈值先轮转。轮转失败与写入失败都以 error
// 返回（slog handler 会丢弃该条记录但不中断进程），不 panic、不在 Write
// 内部递归打日志。句柄失效/写失败后按 retryInterval 节流重开，恢复后
// 后续记录继续落盘。
func (s *FileSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, os.ErrClosed
	}
	if s.file == nil {
		if err := s.reopenIfDueLocked(); err != nil {
			return 0, err
		}
	}
	if s.currentSize > 0 && s.currentSize+int64(len(p)) > s.maxFileBytes {
		if err := s.rotateLocked(); err != nil {
			return 0, err
		}
	}
	written, err := s.file.Write(p)
	s.currentSize += int64(written)
	if err != nil {
		// 句柄可能已失效（NFS 断连/ESTALE）：关闭并置空，下一次按节流重开。
		_ = s.file.Close()
		s.file = nil
		s.scheduleReopenLocked()
		return written, fmt.Errorf("写入运行日志文件 %s 失败: %w", s.currentPath, err)
	}
	return written, nil
}


// Close 幂等关闭 current 文件句柄。
func (s *FileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// Dir 返回日志目录（诊断用）。
func (s *FileSink) Dir() string {
	return s.dir
}

// CurrentPath 返回 current 文件的绝对/相对路径（诊断与启动事件用）。
func (s *FileSink) CurrentPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentPath
}

// CurrentSize 返回 current 文件的已写字节数（诊断与测试用）。
func (s *FileSink) CurrentSize() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentSize
}

// openCurrentLocked 以 append 模式打开 current 文件并回读大小；调用方须已
// 持有 mu。
func (s *FileSink) openCurrentLocked() error {
	path := filepath.Join(s.dir, s.fileName)
	handle, err := s.openFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return fmt.Errorf("打开运行日志文件 %s 失败: %w", path, err)
	}
	info, err := handle.Stat()
	if err != nil {
		_ = handle.Close()
		return fmt.Errorf("读取运行日志文件大小 %s 失败: %w", path, err)
	}
	s.currentPath = path
	s.file = handle
	s.currentSize = info.Size()
	s.nextReopenAt = time.Time{}
	return nil
}

// scheduleReopenLocked 记录下一次允许重开的时间点；调用方须已持有 mu。
func (s *FileSink) scheduleReopenLocked() {
	s.nextReopenAt = s.now().Add(s.retryInterval)
}

// reopenIfDueLocked 在句柄为空时按节流间隔尝试重开；调用方须已持有 mu。
// 节流期内返回不可用错误（附带上次路径信息），到点后真正 open 一次。
func (s *FileSink) reopenIfDueLocked() error {
	now := s.now()
	if !s.nextReopenAt.IsZero() && now.Before(s.nextReopenAt) {
		return fmt.Errorf("运行日志文件 %s 暂不可用（等待重开，下次重试 %s）", s.currentPath, s.nextReopenAt.Format(time.RFC3339))
	}
	if err := s.openCurrentLocked(); err != nil {
		s.scheduleReopenLocked()
		return err
	}
	return nil
}

// rotateLocked 执行 close → rename → 重开 current；调用方须已持有 mu。
// rename 失败时尽力重开 current：失败只丢触发轮转的这条记录，不把文件路
// 永久断供。
func (s *FileSink) rotateLocked() error {
	if s.file != nil {
		if err := s.file.Close(); err != nil {
			s.file = nil
			return fmt.Errorf("关闭运行日志文件 %s 失败: %w", s.currentPath, err)
		}
		s.file = nil
	}
	rotated := filepath.Join(s.dir, rotatedLogFileName(s.fileName, s.now(), s.newToken()))
	if err := os.Rename(s.currentPath, rotated); err != nil {
		reopenErr := s.openCurrentLocked()
		renamed := fmt.Errorf("轮转运行日志文件到 %s 失败: %w", rotated, err)
		if reopenErr != nil {
			s.scheduleReopenLocked()
			return errors.Join(renamed, reopenErr)
		}
		return renamed
	}
	if err := s.openCurrentLocked(); err != nil {
		s.scheduleReopenLocked()
		return err
	}
	s.pruneLocked()
	return nil
}

// rotatedLogFileName 产出与 Node rotatedLogFileName (archived
// shared/logger.ts:552-560) 同款的轮转名：<base>.<YYYYMMDDTHHMMSSZ>.<token>.log。
// 时间戳取 UTC；jobs 消费面正则 ^(.*)\.(\d{8}T\d{6}Z)\.([0-9a-f-]+)\.log$
// 据此识别轮转文件，任何一侧偏离都会让该文件永远留在目录里不被索引/清理。
func rotatedLogFileName(fileName string, timestamp time.Time, token string) string {
	base := strings.TrimSuffix(fileName, ".log")
	return base + "." + timestamp.UTC().Format("20060102T150405Z") + "." + token + ".log"
}

// pruneLocked 清理本实例的 rotated 文件：超过 MaxFiles 的最旧文件删除，超过
// Retention 的文件删除。只匹配 `<base>.<YYYYMMDDTHHMMSSZ>.<token>.log`，不会
// 误删 current 文件或同目录其它实例的日志。调用方须持有 mu。
func (s *FileSink) pruneLocked() {
	base := strings.TrimSuffix(s.fileName, ".log")
	pattern := regexp.MustCompile(`^` + regexp.QuoteMeta(base) + `\.\d{8}T\d{6}Z\.[0-9a-f-]+\.log$`)
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	type candidate struct {
		path    string
		modTime time.Time
	}
	cutoff := s.now().Add(-s.retention)
	candidates := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !pattern.MatchString(entry.Name()) {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		path := filepath.Join(s.dir, entry.Name())
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
			continue
		}
		candidates = append(candidates, candidate{path: path, modTime: info.ModTime()})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].modTime.Equal(candidates[j].modTime) {
			return candidates[i].modTime.After(candidates[j].modTime)
		}
		return candidates[i].path > candidates[j].path
	})
	for index := s.maxFiles; index < len(candidates); index++ {
		_ = os.Remove(candidates[index].path)
	}
}

// newRandomToken 生成 8-4-4-4-12 小写 hex token（UUID 形状，满足消费面
// [0-9a-f-]+ 正则），不引入第三方 uuid 依赖。crypto/rand.Read 自 Go 1.24
// 起契约保证填满整个切片且不返回错误，无需失败分支。
func newRandomToken() string {
	var buffer [16]byte
	_, _ = rand.Read(buffer[:])
	text := hex.EncodeToString(buffer[:])
	return text[0:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:32]
}

// TeeWriter 把每次写入复制到全部 writer。与 io.MultiWriter 的关键差异：
// 某一路失败不短路其余路（stdout 坏管道不得饿死文件路），返回第一个遇到
// 的错误（全部成功返回 nil）。返回写入长度恒为 len(p)：多路写入没有单一
// 语义的部分计数，失败记录由 slog handler 整条丢弃。
type TeeWriter struct {
	writers []io.Writer
}

// NewTeeWriter 复制参数切片，返回的 writer 不受调用方后续修改影响。
func NewTeeWriter(writers ...io.Writer) *TeeWriter {
	return &TeeWriter{writers: append([]io.Writer(nil), writers...)}
}

// Write 逐路写入所有 writer，聚合第一个错误。
func (t *TeeWriter) Write(p []byte) (int, error) {
	var firstErr error
	for _, writer := range t.writers {
		if _, err := writer.Write(p); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return len(p), firstErr
}
