package processlog

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	// MaxFileBytes 是大小轮转阈值；<=0 时取 DefaultMaxFileBytes。
	MaxFileBytes int64
	// Now 与 NewToken 是轮转文件名的时间戳/token 注入点（测试用）。nil 时
	// 分别取 UTC time.Now 与 crypto/rand 派生的 8-4-4-4-12 小写 hex。
	Now      func() time.Time
	NewToken func() string
}

// FileSink 是按大小滚动的 append-only JSONL 文件写侧。轮转判定与 Node
// RotatingLogStream._write 相同：currentSize > 0 且追加后超出阈值才轮转，
// 因此单条超阈记录仍完整写入而非拒写。文件清理由 jobs runtimelog 索引器的
// 保留清理负责，写侧不做任何删除。Write 保证每条记录一次 write 落盘
// (O_APPEND 单写原子性)，进程内不会出现交叉写撕裂行。
type FileSink struct {
	mu           sync.Mutex
	dir          string
	maxFileBytes int64
	now          func() time.Time
	newToken     func() string
	currentPath  string
	file         *os.File
	currentSize  int64
	closed       bool
}

// NewFileSink 创建（必要时）并打开 dir 下的 current 日志文件，回读现有
// 大小作为轮转基线（进程重启后从真实文件大小继续判定阈值）。目录创建或
// 文件打开失败返回 error，由调用方决定降级路径。
func NewFileSink(dir string, options FileSinkOptions) (*FileSink, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建运行日志目录 %s 失败: %w", dir, err)
	}
	sink := &FileSink{
		dir:          dir,
		maxFileBytes: options.MaxFileBytes,
		now:          options.Now,
		newToken:     options.NewToken,
	}
	if sink.maxFileBytes <= 0 {
		sink.maxFileBytes = DefaultMaxFileBytes
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
	return sink, nil
}

// Write 追加一条记录；写前按阈值先轮转。轮转失败与写入失败都以 error
// 返回（slog handler 会丢弃该条记录但不中断进程），不 panic、不在 Write
// 内部递归打日志。
func (s *FileSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, os.ErrClosed
	}
	if s.file == nil {
		return 0, fmt.Errorf("运行日志文件 %s 不可用（此前轮转失败未恢复）", s.currentPath)
	}
	if s.currentSize > 0 && s.currentSize+int64(len(p)) > s.maxFileBytes {
		if err := s.rotateLocked(); err != nil {
			return 0, err
		}
	}
	written, err := s.file.Write(p)
	s.currentSize += int64(written)
	if err != nil {
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
	path := filepath.Join(s.dir, CurrentLogFileName)
	handle, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
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
	rotated := filepath.Join(s.dir, rotatedLogFileName(CurrentLogFileName, s.now(), s.newToken()))
	if err := os.Rename(s.currentPath, rotated); err != nil {
		reopenErr := s.openCurrentLocked()
		renamed := fmt.Errorf("轮转运行日志文件到 %s 失败: %w", rotated, err)
		if reopenErr != nil {
			return errors.Join(renamed, reopenErr)
		}
		return renamed
	}
	return s.openCurrentLocked()
}

// rotatedLogFileName 产出与 Node rotatedLogFileName (archived
// shared/logger.ts:552-560) 同款的轮转名：<base>.<YYYYMMDDTHHMMSSZ>.<token>.log。
// 时间戳取 UTC；jobs 消费面正则 ^(.*)\.(\d{8}T\d{6}Z)\.([0-9a-f-]+)\.log$
// 据此识别轮转文件，任何一侧偏离都会让该文件永远留在目录里不被索引/清理。
func rotatedLogFileName(fileName string, timestamp time.Time, token string) string {
	base := strings.TrimSuffix(fileName, ".log")
	return base + "." + timestamp.UTC().Format("20060102T150405Z") + "." + token + ".log"
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
