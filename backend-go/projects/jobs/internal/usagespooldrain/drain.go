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
//   - 校验通过经 usagewriter.Writer.Enqueue 入队；Enqueue 返回 nil 即视为
//     接受（分片写 ON CONFLICT DO NOTHING，重复投递天然幂等），删除文件；
//     Enqueue 失败（writer 已停等瞬态）保留文件、终止本轮并按固定退避重试
//     （at-least-once，head-of-line 与 Node 本地队列 flush 语义一致）；
//   - 停机时有界排空后返回，未消费文件持久留待下次启动。
//
// 节拍对齐 record_maintenance_jobs drain 族：100ms 轮询、失败固定 1s 退避、
// 停机有界排空（internal/recordmaintenance/drain.go 同形）。
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
// 损坏文件隔离 .corrupt 后继续；入队或删除失败保留现场并终止本轮（该文件
// 与后续文件由调用方按固定退避重试）。返回本轮接受并删除的文件数。
func (d *Drainer) DrainOnce(ctx context.Context) (int, error) {
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
			return processed, err
		}
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
			return processed
		}
	}
	return processed
}

// Run 是 drain 循环（supervisor 组件关闭面）：flushInterval 节拍排空；失败
// 按固定退避后进入下一轮；ctx 取消后做一次有界停机排空再返回。
func (d *Drainer) Run(ctx context.Context) {
	ticker := time.NewTicker(d.flushInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			d.DrainShutdown()
			return
		case <-ticker.C:
			runCtx, cancel := context.WithTimeout(context.Background(), runRoundTimeout)
			_, err := d.DrainOnce(runCtx)
			cancel()
			if err != nil {
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
