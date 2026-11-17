package usagewriter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// BUG-0367 根治（第二层）：死信终态必须与溢出回放目录隔离。
//
// T207-C2 曾让 deadLetterBatch 把耗尽重试的批次写进 FileOverflowSpool，而
// usagespooldrain.Drainer 扫描的正是同一目录树——死信被当作溢出记录回放
// 重投：写库再次失败 → 再次死信 → 再次落盘，形成没有终态的
// 「死信 → 回放 → 再失败」无限环（2026-11-17 生产事故：207 批死信、
// 163,326 条次、统计聚合停摆 6.5h+）。
//
// FileDeadLetterSpool 把死信写到独立根目录（生产装配为
// <DATA_DIR>/usage-record-dead-letter/<instance>/），回放器不扫描该目录。
// 文件名由 usageRecordId 派生，重复落盘按同名覆盖（幂等），因此重试/重启
// 不会在隔离区堆积重复副本；内容保留完整 UsageRecordInput JSON，供人工
// 修正后按文件内容重新入库。

// DeadLetterSpool 是死信终态持久化端口。
type DeadLetterSpool interface {
	Persist(ctx Ctx, input UsageRecordInput) error
}

// FileDeadLetterSpool 是死信终态的文件实现（单文件单记录，格式与
// FileOverflowSpool 一致，便于人工用同一套工具解析）。
type FileDeadLetterSpool struct {
	// Directory 是死信根目录（组合根按 DATA_DIR 派生
	// <DATA_DIR>/usage-record-dead-letter，与 usage-record-overflow-spool 同级）。
	Directory string
	// InstanceDirectory 是本进程死信子目录名（gateway 装配设 "gateway"，
	// jobs 装配空值回落 "jobs"）。
	InstanceDirectory string
	// Clock 供空 ID 记录的文件名时间戳；nil 用 SystemClock。
	Clock Clock

	mu sync.Mutex
}

// NewFileDeadLetterSpool 构建文件死信终态 spool（实例目录默认 "jobs"）。
func NewFileDeadLetterSpool(directory string) *FileDeadLetterSpool {
	return &FileDeadLetterSpool{Directory: directory, Clock: SystemClock{}}
}

const deadLetterInstanceDirectory = "jobs"

func (s *FileDeadLetterSpool) instanceDirectory() string {
	if s.InstanceDirectory == "" {
		return deadLetterInstanceDirectory
	}
	return s.InstanceDirectory
}

// Persist 实现 DeadLetterSpool：把单条记录原子写入死信目录。
func (s *FileDeadLetterSpool) Persist(_ Ctx, input UsageRecordInput) error {
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
	directory := filepath.Join(s.Directory, s.instanceDirectory())
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	name := deadLetterFileName(input, clock)
	temporaryPath := filepath.Join(directory, "."+name+".tmp")
	finalPath := filepath.Join(directory, name)
	if err := os.WriteFile(temporaryPath, encoded, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	return nil
}

// deadLetterFileName 由 usageRecordId 派生稳定文件名：同一条记录重复落盘
// 覆盖同一文件（重试与重启不产生副本）。ID 为空时用时间戳+随机串兜底。
func deadLetterFileName(input UsageRecordInput, clock Clock) string {
	token := sanitizeFileToken(input.ID)
	if token == "" {
		token = "unidentified-" + itoa64(clock.Now().UnixMilli()) + "-" + NewRandomUUID()
	}
	if len(token) > 120 {
		token = token[:120]
	}
	return token + ".json"
}

// sanitizeFileToken 把记录 ID 收敛为安全文件名（保留字母数字与 . - _）。
func sanitizeFileToken(value string) string {
	var builder strings.Builder
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z',
			char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9',
			char == '.', char == '-', char == '_':
			builder.WriteRune(char)
		default:
			builder.WriteByte('_')
		}
	}
	return builder.String()
}
