// Package usagespooldrain 把 gateway 的 usage-record 文件 spool 交接表接到
// jobs usagewriter.Writer（BUG-0175 D-72：用量记录投递链在组合根断开）。
//
// gateway（cmd/juhe-ai-gateway chain_usage.go deliver）把 /v1 用量记录原子写
// 入 <JUHE_AI_USAGE_SPOOL_DIRECTORY>/<instanceID>/<ts>-<pid>-<uuid>.json
// （internal/gatewayusage spool.go Persist，单文件单记录，json.Marshal +
// '\n'，先归一化再落盘）。生产环境里 StartReplay 从未被调用、jobs 不读该
// 目录，usage_records 由此断供；本包是交接表的消费侧：
//   - 扫描 spool 根目录下全部实例子目录的 .json 文件（.tmp/.corrupt 不取），
//     按路径排序（文件名以 UnixMilli 开头，近似写入序）；
//   - 逐文件解析单条 JSON 用量记录，复用 usagewriter records.go 的归一化/
//     校验路径（NormalizeUsageRecordInput）；解析失败、缺稳定 id/createdAt、
//     归一化校验失败的文件对齐 gateway spool.go 自身语义隔离为
//     <原路径>.corrupt（保留现场，不阻塞后续文件）；
//   - 读取失败（非 ErrNotExist，如 BUG-0227 root→app 切换窗口残留的
//     root:root 0600 文件致 EACCES）的文件不隔离、不删除：记节流 Error
//     （event=usage_record_spool_file_read_failed，同文件 60s 窗口内不
//     重复）后跳过，继续本轮后续文件，运维修复属主/权限后下轮自然消费
//     （区别于损坏文件的 .corrupt 隔离语义）；
//   - 校验通过经 usagewriter.Writer.Enqueue 入队；Enqueue 返回 nil 即视为
//     接受并删除文件。接受语义 = 记录已进入 writer 内存队列，或（队列满
//     时）已同步溢出落盘到 overflow spool 文件（落盘成功 Enqueue 才返回，
//     可回放不丢）。删除后、writer 批量落库（默认 500ms flush）前进程崩
//     溃的内存窗口是已知取舍（Node 本地队列同款边界）；Enqueue 失败
//     （writer 已停等瞬态）保留文件、终止本轮并按固定退避重试
//     （at-least-once，head-of-line 与 Node 本地队列 flush 语义一致）；
//   - 每轮结束时刷新待删水位（OldestPendingCreatedAt）：按文件名序取第
//     一个可读待消费 spool 文件内记录的 created_at 反馈给 ingestgate 游
//     标安全门，使统计游标在 drain 仍持有未确认文件时不得越过其中最旧记
//     录（否则记录滞留超过安全水位后入表，statsagg 严格单调游标已越过其
//     created_at，永久不聚合）。不可读队头（非 ErrNotExist 读取失败，
//     BUG-0227）直接跳过，统计游标安全门不得被属主异常文件永久卡死；仅
//     剩不可读文件时保留上次水位（不前进也不清空，取舍见
//     refreshPendingWatermark 注释）。排序假设：文件名以 UnixMilli 开头，
//     gateway 侧每记录落盘即写、互斥串行（FIFO），文件名序即最旧记录；
//     跨实例时钟偏斜与 gateway 进程内 buffer 的残余窗口同源（进程边界
//     外，Node 同款边界）。
//   - 停机时有界排空后返回，未消费文件持久留待下次启动。
//
// 节拍对齐 record_maintenance_jobs drain 族：100ms 轮询、失败固定 1s 退避、
// 停机有界排空（internal/recordmaintenance/drain.go 同形）；失败轮打 Warn
// （event=usage_record_spool_drain_round_failed，BUG-0227：排空失败不得
// 静默退避）。
package usagespooldrain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/usagewriter"
)

// drain 节拍与结算常量（record_maintenance drain 族同值；BatchSize 取
// gateway SpoolConfig.ReplayBatchSize 同值）。
const (
	// DefaultFlushIntervalMs 对齐 recordMaintenanceFlushIntervalMs（100ms）。
	DefaultFlushIntervalMs = 100
	// DefaultBatchSize 对齐 gateway usage spool ReplayBatchSize（500 文件/轮）。
	DefaultBatchSize = 500
	// DefaultRetryDelay 对齐 fixedRetryPolicy 固定重试延迟（1s）。
	DefaultRetryDelay = time.Second
	// DefaultShutdownFlushMaxBatches 对齐
	// recordMaintenanceShutdownFlushMaxBatches（1 批；未消费文件持久留待
	// 下次启动，停机只做有界收尾）。
	DefaultShutdownFlushMaxBatches = 1

	// runRoundTimeout 是单轮 drain 的执行上限（对照组合根 flushLoop 每批
	// 60s 的既有约定）。
	runRoundTimeout = 60 * time.Second
	// shutdownRoundTimeout 是停机排空每批上限（对照组合根 queue.drainShutdown
	// 每批 30s 的既有约定）。
	shutdownRoundTimeout = 30 * time.Second
	// drainSummaryInterval 是 Run 循环 Info 级排空汇总的最小窗口：排空节拍
	// 默认 100ms，逐轮 Info 在持续流量下会刷屏，Info 只按此窗口输出累计值。
	drainSummaryInterval = time.Minute
	// defaultReadFailureLogWindow 是不可读文件（ReadFile 非 ErrNotExist
	// 失败）错误日志的同文件节流窗口：drain 节拍 100ms，不节流会在持续
	// 故障下每秒 × 文件数刷屏（BUG-0227）。
	defaultReadFailureLogWindow = 60 * time.Second
	// readFailureLogPruneAt 触发读取失败节流表清理的规模阈值（防长期
	// 运行下被已消费/已消失文件的残留条目积累）。
	readFailureLogPruneAt = 1024
)

// Enqueuer 是 usagewriter.Writer.Enqueue 的窄口（接口化供测试 Mock）。
// 返回 nil 表示记录已被接受（入队成功或幂等重复）；非 nil 表示瞬态失败，
// 文件保留重试。
type Enqueuer interface {
	Enqueue(ctx usagewriter.Ctx, input usagewriter.UsageRecordInput) error
}

// Drainer 轮询 gateway usage spool 目录并入队用量记录。
type Drainer struct {
	// Directory 是 spool 根目录（与 gateway JUHE_AI_USAGE_SPOOL_DIRECTORY
	// 同值或同派生规则：未配置 env 时取 <stats 库目录>/usage-record-spool）。
	Directory string
	// Enqueuer 是用量记录入队口（生产装配为 *usagewriter.Writer）。
	Enqueuer Enqueuer
	// Logger 接收 drain 事件（隔离/失败/停机）；nil 用 slog.Default()。
	Logger *slog.Logger

	// BatchSize / FlushInterval / RetryDelay / ShutdownFlushMaxBatches 为
	// 零值时取上方默认。
	BatchSize               int
	FlushInterval           time.Duration
	RetryDelay              time.Duration
	ShutdownFlushMaxBatches int

	// oldestMu 保护待删水位；drain goroutine 写、ingestgate probe 读。
	oldestMu               sync.Mutex
	oldestPendingCreatedAt string

	// readFailMu 保护 readFailLast：不可读文件错误日志的同文件节流表
	// （路径 → 上次记录时间，BUG-0227）。
	readFailMu   sync.Mutex
	readFailLast map[string]time.Time
}

func (d *Drainer) batchSize() int {
	if d.BatchSize > 0 {
		return d.BatchSize
	}
	return DefaultBatchSize
}

func (d *Drainer) flushInterval() time.Duration {
	if d.FlushInterval > 0 {
		return d.FlushInterval
	}
	return DefaultFlushIntervalMs * time.Millisecond
}

func (d *Drainer) retryDelay() time.Duration {
	if d.RetryDelay > 0 {
		return d.RetryDelay
	}
	return DefaultRetryDelay
}

func (d *Drainer) shutdownFlushMaxBatches() int {
	if d.ShutdownFlushMaxBatches > 0 {
		return d.ShutdownFlushMaxBatches
	}
	return DefaultShutdownFlushMaxBatches
}

func (d *Drainer) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.Default()
}

// listSpoolFiles 收集 spool 根目录下全部实例子目录的 .json 文件（gateway
// listInstanceDirectories/listSpoolFiles 的读取侧镜像：实例子目录是
// gateway 的 InstanceID 隔离层；.tmp/.corrupt 不在消费面），排序后截取批次。
func (d *Drainer) listSpoolFiles(limit int) ([]string, error) {
	entries, err := os.ReadDir(d.Directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		instanceDirectory := filepath.Join(d.Directory, entry.Name())
		instanceEntries, err := os.ReadDir(instanceDirectory)
		if err != nil {
			return nil, err
		}
		for _, instanceEntry := range instanceEntries {
			if instanceEntry.IsDir() || !strings.HasSuffix(instanceEntry.Name(), ".json") {
				continue
			}
			files = append(files, filepath.Join(instanceDirectory, instanceEntry.Name()))
		}
	}
	sort.Strings(files)
	if len(files) > limit {
		files = files[:limit]
	}
	return files, nil
}

// parseSpoolRecord 解析单文件单记录（gateway Persist 写出格式）并复用
// usagewriter 归一化路径校验：缺稳定 id/createdAt 或校验失败视同 gateway
// parseUsageRecord 的损坏语义（隔离 .corrupt）。clock/idFactory 传 nil 安全：
// 两者只在 id/createdAt 为空时被用到，而空值已在此前置拒绝。
func parseSpoolRecord(content []byte, filePath string) (usagewriter.UsageRecordInput, error) {
	var input usagewriter.UsageRecordInput
	if err := json.Unmarshal(content, &input); err != nil {
		return usagewriter.UsageRecordInput{}, fmt.Errorf("usage spool 文件格式错误：%s", filepath.Base(filePath))
	}
	if input.ID == "" || input.CreatedAt == "" {
		return usagewriter.UsageRecordInput{}, fmt.Errorf("usage spool 文件缺少稳定 id/createdAt：%s", filepath.Base(filePath))
	}
	normalized, err := usagewriter.NormalizeUsageRecordInput(input, nil, nil)
	if err != nil {
		return usagewriter.UsageRecordInput{}, fmt.Errorf("usage spool 文件记录校验失败：%s", filepath.Base(filePath))
	}
	return normalized, nil
}

// DrainOnce 处理至多一个批次的 spool 文件：解析 → 校验 → 入队 → 删除。
// 损坏文件隔离 .corrupt 后继续；读取失败（非 ErrNotExist，如属主/权限不
// 符）的文件记节流 Error 后跳过、继续本轮后续文件（BUG-0227：不隔离、
// 不删除，运维修复后自然消费）；入队或删除失败保留现场并终止本轮（该文
// 件与后续文件由调用方按固定退避重试）。返回本轮接受并删除的文件数。退
// 出时（含失败早退）刷新待删水位。
func (d *Drainer) DrainOnce(ctx context.Context) (int, error) {
	defer d.refreshPendingWatermark()
	files, err := d.listSpoolFiles(d.batchSize())
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, filePath := range files {
		if err := ctx.Err(); err != nil {
			return processed, err
		}
		content, err := os.ReadFile(filePath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// 并发删除（gateway 容量扫描 / 上轮残留）：跳过。
				continue
			}
			// 不可读（属主/权限不符等）：不隔离、不删除，记节流 Error 后
			// 跳过，继续本轮后续文件（BUG-0227 head-of-line 停摆修复；
			// 运维修复属主后下轮自然消费，区别于 .corrupt 隔离语义）。
			d.logUnreadableSpoolFile(filePath, err)
			continue
		}
		d.forgetReadFailure(filePath)
		input, err := parseSpoolRecord(content, filePath)
		if err != nil {
			d.logger().Warn("usage spool 文件已隔离为损坏文件",
				"event", "usage_record_spool_file_corrupt",
				"file", filePath, "error", err.Error())
			if renameErr := os.Rename(filePath, filePath+".corrupt"); renameErr != nil {
				return processed, renameErr
			}
			continue
		}
		if err := d.Enqueuer.Enqueue(ctx, input); err != nil {
			d.logger().Error("usage spool 投递失败，已保留文件等待重试",
				"event", "usage_record_spool_flush_failed",
				"file", filePath, "usageRecordId", input.ID, "error", err.Error())
			return processed, err
		}
		if err := os.Remove(filePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			// 入队成功但删除失败：文件会在下轮重复入队（分片写按 id
			// ON CONFLICT DO NOTHING 幂等），按失败退避重试。
			d.logger().Error("usage spool 文件删除失败，将由下轮重复投递（幂等）",
				"event", "usage_record_spool_file_remove_failed",
				"file", filePath, "error", err.Error())
			return processed, err
		}
		processed++
	}
	return processed, nil
}

// logUnreadableSpoolFile 记录不可读 spool 文件（ReadFile 非 ErrNotExist 失
// 败）的 Error 日志，按同文件 60s 窗口节流（BUG-0227：drain 节拍 100ms，
// 不节流会在持续故障下每秒 × 文件数刷屏）。节流键取文件路径：同一故障期
// 内同文件的错误形态基本不变，错误形态变化至多延迟一个窗口暴露。
func (d *Drainer) logUnreadableSpoolFile(filePath string, readErr error) {
	now := time.Now()
	d.readFailMu.Lock()
	if d.readFailLast == nil {
		d.readFailLast = make(map[string]time.Time)
	}
	if last, ok := d.readFailLast[filePath]; ok && now.Sub(last) < defaultReadFailureLogWindow {
		d.readFailMu.Unlock()
		return
	}
	d.readFailLast[filePath] = now
	if len(d.readFailLast) > readFailureLogPruneAt {
		for path, at := range d.readFailLast {
			if now.Sub(at) >= defaultReadFailureLogWindow {
				delete(d.readFailLast, path)
			}
		}
	}
	d.readFailMu.Unlock()
	d.logger().Error("usage spool 文件读取失败，已跳过并保留文件（不隔离），待属主/权限修复后重试",
		"event", "usage_record_spool_file_read_failed",
		"file", filePath,
		"error", readErr.Error())
}

// forgetReadFailure 清除文件的读取失败节流记录：读取恢复（运维修复属主）
// 后再次失败允许立即重新暴露，而非再等一个完整窗口。
func (d *Drainer) forgetReadFailure(filePath string) {
	d.readFailMu.Lock()
	delete(d.readFailLast, filePath)
	d.readFailMu.Unlock()
}

// OldestPendingCreatedAt 返回 drain 仍未确认删除的 spool 文件中，按文件
// 名序第一个可读文件内记录的 created_at（RFC3339 毫秒；不可读队头跳过，
// BUG-0227：统计游标安全门不得被属主异常文件永久卡死）；无积压或本轮尚
// 未扫描时返回空串。损坏队头保留上次水位（偏保守：游标多等一轮），队头
// 在下一轮被隔离/删除后自然前进；仅剩不可读文件时同样保留上次水位（见
// refreshPendingWatermark）。
func (d *Drainer) OldestPendingCreatedAt() string {
	d.oldestMu.Lock()
	defer d.oldestMu.Unlock()
	return d.oldestPendingCreatedAt
}

// refreshPendingWatermark 刷新待删水位：按文件名序探测待消费候选，跳过
// 不可读（非 ErrNotExist 读取失败）与并发消失的文件，取第一个可读且解析
// 合法文件记录的 created_at。目录为空时清空水位。仅剩不可读文件时保留上
// 次水位（不前进也不清空）：可读积压已消费完，水位停留在最后一个可读积
// 压点；不可读文件修复入表时其记录晚于该水位、统计聚合可能缺失属已知取
// 舍——故障本身由 DrainOnce 的节流 Error 日志即时暴露（BUG-0227 契约 3）。
func (d *Drainer) refreshPendingWatermark() {
	candidates, err := d.pendingSpoolCandidates()
	if err != nil {
		// 列目录失败（权限等）：保留上次水位，下一轮重试。
		return
	}
	for _, head := range candidates {
		content, err := os.ReadFile(head)
		if err != nil {
			// 并发删除（ErrNotExist，gateway 容量扫描等）与不可读（属主/
			// 权限不符）都试下一个候选：前者文件已消失，后者由 DrainOnce
			// 的节流 Error 日志暴露，水位跳过它前进。
			continue
		}
		record, err := parseSpoolRecord(content, head)
		if err != nil {
			// 损坏队头：下一轮隔离 .corrupt 后队头前进；保留上次水位。
			return
		}
		d.setOldestPendingCreatedAt(record.CreatedAt)
		return
	}
	if len(candidates) == 0 {
		d.setOldestPendingCreatedAt("")
		return
	}
	// 全部候选不可读或并发消失：保留上次水位（见函数注释）。
}

func (d *Drainer) setOldestPendingCreatedAt(value string) {
	d.oldestMu.Lock()
	d.oldestPendingCreatedAt = value
	d.oldestMu.Unlock()
}

// pendingSpoolCandidates 返回全部待消费 spool 文件路径，按文件名升序
// （原 oldestSpoolFilePath 的队头选取语义收敛为全量候选列表：水位探测
// 需要逐个跳过不可读队头，BUG-0227）。文件名以 UnixMilli 开头，gateway
// 侧每记录落盘即写、互斥串行（FIFO），文件名序即最旧记录；跨实例按文
// 件名比较（实例目录名不参与排序）。
func (d *Drainer) pendingSpoolCandidates() ([]string, error) {
	entries, err := os.ReadDir(d.Directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		instanceDirectory := filepath.Join(d.Directory, entry.Name())
		instanceEntries, err := os.ReadDir(instanceDirectory)
		if err != nil {
			return nil, err
		}
		for _, instanceEntry := range instanceEntries {
			if instanceEntry.IsDir() || !strings.HasSuffix(instanceEntry.Name(), ".json") {
				continue
			}
			files = append(files, filepath.Join(instanceDirectory, instanceEntry.Name()))
		}
	}
	sort.Slice(files, func(i, j int) bool {
		nameI, nameJ := filepath.Base(files[i]), filepath.Base(files[j])
		if nameI != nameJ {
			return nameI < nameJ
		}
		return files[i] < files[j]
	})
	return files, nil
}

// DrainShutdown 停机排空：最多 maxBatches 个批次、失败即停，剩余文件持久
// 留待下次启动消费。返回成功接受的文件数。
func (d *Drainer) DrainShutdown() int {
	processed := 0
	for batch := 0; batch < d.shutdownFlushMaxBatches(); batch++ {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownRoundTimeout)
		settled, err := d.DrainOnce(ctx)
		cancel()
		processed += settled
		if err != nil {
			d.logger().Warn("usage spool 停机排空中止，剩余文件留待下次启动",
				"event", "usage_record_spool_shutdown_failed", "error", err.Error())
			return processed
		}
		if settled == 0 {
			d.logger().Debug("usage spool 停机排空完成",
				"event", "usage_record_spool_shutdown_done", "processedFiles", processed)
			return processed
		}
	}
	d.logger().Debug("usage spool 停机排空完成",
		"event", "usage_record_spool_shutdown_done", "processedFiles", processed)
	return processed
}

// Run 是 drain 循环（supervisor 组件关闭面）：flushInterval 节拍排空；失败
// 轮先打 Warn（event=usage_record_spool_drain_round_failed，BUG-0227：不
// 得静默退避）再按固定退避进入下一轮；ctx 取消后做一次有界停机排空再返
// 回。排空节拍默认 100ms，持续流量下"本轮结果"走 Debug（默认级别不输
// 出），Info 只按 drainSummaryInterval 窗口输出一条累计汇总，避免常规遥
// 测刷屏。
func (d *Drainer) Run(ctx context.Context) {
	ticker := time.NewTicker(d.flushInterval())
	defer ticker.Stop()
	var (
		windowFiles  int64
		windowRounds int
		windowStart  = time.Now()
	)
	for {
		select {
		case <-ctx.Done():
			d.DrainShutdown()
			return
		case <-ticker.C:
			roundStart := time.Now()
			runCtx, cancel := context.WithTimeout(context.Background(), runRoundTimeout)
			processed, err := d.DrainOnce(runCtx)
			cancel()
			if processed > 0 {
				windowFiles += int64(processed)
				windowRounds++
				d.logger().Debug("usage spool 本轮排空完成",
					"event", "usage_record_spool_drain_round",
					"processedFiles", processed, "elapsedMs", time.Since(roundStart).Milliseconds())
				if time.Since(windowStart) >= drainSummaryInterval {
					d.logger().Info("usage spool 排空窗口汇总",
						"event", "usage_record_spool_drain_summary",
						"windowFiles", windowFiles, "windowRounds", windowRounds,
						"windowMs", time.Since(windowStart).Milliseconds())
					windowFiles, windowRounds = 0, 0
					windowStart = time.Now()
				}
			}
			if err != nil {
				// BUG-0227：失败轮不得静默退避——排空轮错误（列目录、隔
				// 离、入队、删除失败等）必须有 Warn 遥测面。
				d.logger().Warn("usage spool 排空轮失败，退避后重试",
					"event", "usage_record_spool_drain_round_failed",
					"error", err.Error(), "processed", processed)
				timer := time.NewTimer(d.retryDelay())
				select {
				case <-ctx.Done():
					timer.Stop()
					d.DrainShutdown()
					return
				case <-timer.C:
				}
			}
		}
	}
}
