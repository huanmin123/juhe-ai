package usagewriter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// FileOverflowSpool 是 OverflowSpool 的文件实现（组合根生产装配的唯一
// 溢出补偿；历史装配未调 WithOverflowSpool，队列满时记录直接丢弃且无处
// 恢复）。写出格式与 gateway internal/gatewayusage spool.go Persist 完全
// 同构：单文件单记录 json.Marshal + '\n'，原子 temp+rename 落盘，文件名
// `<UnixMilli>-<pid>-<uuid>.json`（UnixMilli 前缀即持久化时刻，按名排序
// 近似写入序），实例子目录固定 "jobs"（与 gateway 的 InstanceID 隔离层同
// 形）。同构的目的是让 internal/usagespooldrain.Drainer 无改动复用为回放
// 消费方：解析、校验、幂等入队（分片写 ON CONFLICT DO NOTHING）、删除的
// 既有语义原样生效。
//
// 入队侧契约：溢出记录在 Enqueue 内已经 NormalizeUsageRecordInput 归一化，
// 稳定 id/createdAt 必非空，drain 侧 parseSpoolRecord 的前置校验恒通过。
type FileOverflowSpool struct {
	// Directory 是溢出 spool 根目录（组合根按 stats 库目录派生
	// <目录>/usage-record-overflow-spool，与 usage-record-spool 同级）。
	Directory string
	// Clock 供文件名时间戳；nil 用 SystemClock。
	Clock Clock

	mu sync.Mutex
}

// NewFileOverflowSpool 构建文件溢出 spool。
func NewFileOverflowSpool(directory string) *FileOverflowSpool {
	return &FileOverflowSpool{Directory: directory, Clock: SystemClock{}}
}

// overflowSpoolInstanceDirectory 是本进程溢出文件的实例子目录名（drain
// 消费面扫描全部实例子目录，名字只需稳定且与 gateway 实例目录不混淆）。
const overflowSpoolInstanceDirectory = "jobs"

// Persist 实现 OverflowSpool：把单条记录原子写入溢出目录。返回非 nil 时
// 由 writer 按 dispatch 失败计数（见 Writer.spoolOverflow）。
func (s *FileOverflowSpool) Persist(_ Ctx, input UsageRecordInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clock := s.Clock
	if clock == nil {
		clock = SystemClock{}
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	directory := filepath.Join(s.Directory, overflowSpoolInstanceDirectory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	token := itoa64(clock.Now().UnixMilli()) + "-" + itoa(os.Getpid()) + "-" + NewRandomUUID()
	temporaryPath := filepath.Join(directory, "."+token+".tmp")
	finalPath := filepath.Join(directory, token+".json")
	if err := os.WriteFile(temporaryPath, encoded, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	return nil
}
