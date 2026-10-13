// studiosettle 的 worker 核心（媒体工作室设计 §5）：领取谓词 + 原子租约 +
// 有界轮询 + 幂等三道闸结算 + 崩溃恢复 + 取消迟到成功收口。
package studiosettle

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// 契约常量（§5.1/§5.3；调整属代码常量修订，随交付同步契约，不设开关）。
const (
	// LeaseTTL 是任务租约时长（§5.1：租约 60s；过期后其他实例可接管——崩溃
	// 恢复的基本机制）。
	LeaseTTL = 60 * time.Second
	// DefaultBatchSize 是每轮领取行上限（任务批次有界 ≤20）。
	DefaultBatchSize = 20
	// MaxLeaseAttempts 是 lease_attempt_count 领取/结算尝试上限（§5.1 领取
	// 谓词 lease_attempt_count < 5；超限终局 settlement='failed' 不再领取）。
	MaxLeaseAttempts = 5
	// PollInterval 是单任务轮询间隔（§5.3：5s）。
	PollInterval = 5 * time.Second
	// DefaultMaxPollsPerClaim 是单次领取内的轮询次数上限（5s 间隔 × 60 =
	// 5 分钟轮询窗口；窗口内未终态则归还（租约自然过期），下一轮重新领取。
	// 尝试上限 × 窗口 = 单任务最长结算覆盖 ≈ 25 分钟，超过后终局失败——
	// 有界、不永久重试；attempt 计数按领取（租约恢复周期）有界，而非轮询拍）。
	DefaultMaxPollsPerClaim = 60
	// VideoDownloadLimit / ImageDownloadLimit 是产物下载字节上限（gateway
	// studio 单资产上限 512MiB / 图像链 16MiB 同值镜像）。
	VideoDownloadLimit = int64(512) << 20
	ImageDownloadLimit = int64(16) << 20
)

// SettleJob 是 studio-tasks-settle 的任务体（组合根经 scheduleWiredJob 装配；
// 依赖缺席显式报错不静默）。
type SettleJob struct {
	// Store 是 studio_tasks 结算仓储（双模 SQL）；nil 显式报错。
	Store *TaskStore
	// Accounts 水合账户亲和运行时（组合根实现：accounts 行 + 凭据封套解密）。
	Accounts AccountResolver
	// Upstream 是上游轮询/下载客户端（与 gateway 任务面同 URL 形态）。
	Upstream *UpstreamClient
	// Objects 是产物对象写删端口。
	Objects ObjectWriter
	// Clock 注入时间源（测试可固定）。
	Clock func() time.Time
	// Logger 缺省 slog.Default()。
	Logger *slog.Logger
	// LeaseOwner 是领取者身份（组合根传实例标识）。
	LeaseOwner string
	// BatchSize 每轮领取行上限（缺省 DefaultBatchSize）。
	BatchSize int
	// LeaseTTL 租约时长（缺省 LeaseTTL）。
	LeaseTTL time.Duration
	// MaxLeaseAttempts 领取/结算尝试上限（缺省 MaxLeaseAttempts）。
	MaxLeaseAttempts int
	// MaxPollsPerClaim 单次领取内轮询次数上限（缺省 DefaultMaxPollsPerClaim）。
	MaxPollsPerClaim int
	// PollEvery 单任务轮询间隔（0 = 契约常量 PollInterval 5s；测试注入更短
	// 间隔，非功能开关——只改节拍不改行为面）。
	PollEvery time.Duration
}

// Result 是单轮结算计数。
type Result struct {
	// Exhausted 是 attempt 上限收口行数（§5.1 终局 failed）。
	Exhausted int64
	// Claimed 是本轮领取行数。
	Claimed int
	// SettledWithAsset 是产物入库收口行数（含 digest 复用既有资产行的重放）。
	SettledWithAsset int
	// ReusedAssets 是其中复用既有资产行（闸 3 命中）的行数。
	ReusedAssets int
	// SettledNoProduct 是无产物收口行数（取消迟到成功/失败/过期/上游失败）。
	SettledNoProduct int
	// SettlementFailed 是本轮落 settlement='failed'（可重试）的行数。
	SettlementFailed int
	// Expired 是任务面不可达 expired 收口行数。
	Expired int
	// Conflicts 是结算冲突（并发收口/取消竞态，产物已回收）行数。
	Conflicts int
	// StillRunning 是轮询窗口内仍未终态、归还待下轮的行数。
	StillRunning int
	// TransientErrors 是本轮发生的瞬态错误数（网络/5xx/行写失败；行保持
	// pending_settled 由租约过期后重试）。
	TransientErrors int
}

// Run 执行一轮结算：attempt 上限收口 → 原子领取 → 逐任务结算。表缺席（老库
// 未跑 maintenance 迁移）按零任务快速返回。
func (j *SettleJob) Run(ctx context.Context) (Result, error) {
	result := Result{}
	if j.Store == nil {
		return result, fmt.Errorf("studio 结算仓储未初始化")
	}
	if j.Store.DB == nil {
		return result, fmt.Errorf("studio 结算数据库句柄未初始化")
	}
	if j.Accounts == nil || j.Upstream == nil || j.Objects == nil {
		return result, fmt.Errorf("studio 结算依赖未装配（accounts/upstream/objects）")
	}

	// §5.1：达到 attempt 上限仍未结算的行先终局收口（settlement='failed'，
	// 领取谓词此后不再命中），不无限重试、不虚计。
	exhausted, err := j.Store.ExhaustAttempts(ctx, errorJSON(ErrorCodeAttemptsExhaust, "结算重试次数已达上限，产物未入库"), formatNow(j.now()), j.maxAttempts(), DefaultBatchSize)
	if err != nil {
		if IsStudioTableMissing(err) {
			return result, nil
		}
		return result, fmt.Errorf("收口 studio 任务重试上限失败: %w", err)
	}
	result.Exhausted = exhausted

	tasks, err := j.Store.ClaimBatch(ctx, formatNow(j.now()), formatNow(j.now().Add(j.leaseTTL())), j.leaseOwner(), j.batchSize(), j.maxAttempts())
	if err != nil {
		if IsStudioTableMissing(err) {
			return result, nil
		}
		return result, err
	}
	result.Claimed = len(tasks)
	for _, task := range tasks {
		if ctx.Err() != nil {
			break
		}
		j.settleOne(ctx, task, &result)
	}
	if result.Claimed > 0 || result.Exhausted > 0 {
		j.logger().Info("studio 任务结算轮完成",
			"event", "studio_tasks_settle_round",
			"claimed", result.Claimed,
			"settledWithAsset", result.SettledWithAsset,
			"reusedAssets", result.ReusedAssets,
			"settledNoProduct", result.SettledNoProduct,
			"settlementFailed", result.SettlementFailed,
			"expired", result.Expired,
			"conflicts", result.Conflicts,
			"stillRunning", result.StillRunning,
			"transientErrors", result.TransientErrors,
			"exhausted", result.Exhausted)
	}
	return result, nil
}

// settleOne 按任务形态分派（§6 状态机）：
//   - cancelled：取消迟到成功——不轮询上游，直接无产物 settled；
//   - expired：无产物可结算，settled 收口；
//   - failed + settlement<>failed：本地终态无产物（崩溃窗口残留），settled
//     收口；failed + settlement='failed' 是 §6 重领取重试臂——有产物待入库，
//     走产物路径（image 重下载 / video 轮询+下载），status 保持 failed；
//   - 其余（queued/in_progress/completed）：video 走上游轮询 + 产物下载结算；
//     image 走快照 url 重下载一次结算。
func (j *SettleJob) settleOne(ctx context.Context, task *TaskRow, result *Result) {
	now := formatNow(j.now())
	switch {
	case task.Status == StatusCancelled:
		// 取消迟到成功（§4.4/§6）：不轮询上游、无产物可结算；error_json 不
		// 改写（取消不是错误）。
		if _, err := j.Store.SettleNoProduct(ctx, task.ID, task.SystemAccountID, "'cancelled'", "", now); err != nil {
			j.warn("studio 任务取消收口失败", "studio_task_cancel_settle_failed", task.ID, err)
			result.TransientErrors++
			return
		}
		result.SettledNoProduct++
		return
	case task.Status == StatusExpired:
		if _, err := j.Store.SettleNoProduct(ctx, task.ID, task.SystemAccountID, "'expired'", "", now); err != nil {
			j.warn("studio 任务过期收口失败", "studio_task_terminal_settle_failed", task.ID, err)
			result.TransientErrors++
			return
		}
		result.SettledNoProduct++
		return
	case task.Status == StatusFailed && task.Settlement != SettlementFailed:
		if _, err := j.Store.SettleNoProduct(ctx, task.ID, task.SystemAccountID, "'failed'", "", now); err != nil {
			j.warn("studio 任务终态收口失败", "studio_task_terminal_settle_failed", task.ID, err)
			result.TransientErrors++
			return
		}
		result.SettledNoProduct++
		return
	}
	if task.Kind == KindVideo {
		j.settleVideo(ctx, task, result)
		return
	}
	j.settleImage(ctx, task, result)
}

// settleVideo 处理视频任务（§5.1/§5.3）：账户亲和回读 → 直连上游轮询（5s
// 间隔、单领取有界窗口、按进度续租）→ 终态驱动本地收口；completed 经有界
// 下载 + 三道闸事务入库。
func (j *SettleJob) settleVideo(ctx context.Context, task *TaskRow, result *Result) {
	affinity, gone, err := j.videoAffinity(ctx, task)
	if err != nil {
		// 关联确立失败（读库/采纳失败）：可重试，行保持 pending_settled。
		j.warn("studio 任务上游关联确立失败", "studio_task_affinity_failed", task.ID, err)
		result.TransientErrors++
		return
	}
	if gone {
		// media_jobs 行被 7 天保留清理：任务面不可达（§5.3）→ expired 收口。
		if _, err := j.Store.ExpireUnreachable(ctx, task.ID, task.SystemAccountID,
			errorJSON(ErrorCodeUnreachable, "上游任务已过保留期，任务不可达"), formatNow(j.now())); err != nil {
			j.warn("studio 任务不可达收口失败", "studio_task_expire_failed", task.ID, err)
			result.TransientErrors++
			return
		}
		result.Expired++
		return
	}
	if affinity == nil {
		// 受理未确立且重扫未命中：不自动重放创建（直连形态下重新创建属复制
		// 派发逻辑，§5.2 底线；图像同款 studio_acceptance_unknown 语义），
		// 结算终局失败由用户显式重试。
		j.markSettlementFailed(ctx, task, ErrorCodeAcceptanceGone, "任务受理未确立且无法关联上游任务，请重新发起生成", result)
		return
	}

	account, err := j.Accounts.Resolve(ctx, affinity.AccountID)
	if err != nil {
		// 账户凭据读库/解密失败：结算失败可重试（attempt 上限收口）。
		j.markSettlementFailed(ctx, task, ErrorCodeSettleFailed, "上游账户凭据不可用，产物未入库", result)
		return
	}
	if !account.Reachable {
		// 任务面"任务不可达"语义（§5.2）：原账户已删/禁用 → expired 收口，
		// 无产物可结算，不换账户。
		if _, err := j.Store.ExpireUnreachable(ctx, task.ID, task.SystemAccountID,
			errorJSON(ErrorCodeUnreachable, "原账户不可用，任务无法继续结算"), formatNow(j.now())); err != nil {
			j.warn("studio 任务不可达收口失败", "studio_task_expire_failed", task.ID, err)
			result.TransientErrors++
			return
		}
		result.Expired++
		return
	}

	polls := j.maxPolls()
	for attempt := 0; attempt < polls; attempt++ {
		if ctx.Err() != nil {
			result.StillRunning++
			return
		}
		poll, err := j.Upstream.PollVideo(ctx, account, affinity.UpstreamJobID)
		if err != nil {
			var statusErr *UpstreamStatusError
			if errors.As(err, &statusErr) && statusErr.StatusCode == 404 {
				// 上游 404 且本地非终态：保持本地状态直至任务面 TTL 收敛
				//（media_jobs 7 天保留清理后 → 不可达 expired），不伪造终态
				//（媒体契约 §2.6）。书签记录后归还。
				j.saveBookmark(ctx, task, map[string]any{"upstreamStatus": "not_found", "polledAt": formatNow(j.now())})
				result.StillRunning++
				return
			}
			// 5xx / 网络瞬态：本轮不动 settlement，租约过期后重试。
			j.warn("studio 任务上游轮询失败", "studio_task_poll_failed", task.ID, err)
			result.TransientErrors++
			return
		}
		j.saveBookmark(ctx, task, map[string]any{
			"upstreamStatus": poll.Status,
			"rawStatus":      poll.RawStatus,
			"progress":       poll.Progress,
			"polledAt":       formatNow(j.now()),
		})
		switch poll.Status {
		case StatusInProgress, StatusQueued:
			if task.Status == StatusQueued {
				if _, err := j.Store.AdvanceStatus(ctx, task.ID, task.SystemAccountID, StatusInProgress, formatNow(j.now())); err != nil {
					j.warn("studio 任务状态推进失败", "studio_task_advance_failed", task.ID, err)
				}
			}
			// 按进度续租（§5.1），等下一拍继续轮询。
			if _, err := j.Store.RenewLease(ctx, task.ID, j.leaseOwner(), formatNow(j.now().Add(j.leaseTTL())), formatNow(j.now())); err != nil {
				j.warn("studio 任务续租失败", "studio_task_renew_failed", task.ID, err)
			}
			sleepCtx(ctx, j.pollEvery())
			continue
		case StatusFailed:
			// 上游终态失败：镜像 failed + settled（无产物可结算，§3.2）。
			message := "上游生成失败"
			code := "studio_upstream_failed"
			if poll.Error != nil {
				if poll.Error.Message != "" {
					message = poll.Error.Message
				}
				if poll.Error.Code != "" {
					code = poll.Error.Code
				}
			}
			if _, err := j.Store.FailTaskTerminal(ctx, task.ID, task.SystemAccountID, errorJSON(code, message), formatNow(j.now())); err != nil {
				j.warn("studio 任务上游失败收口失败", "studio_task_fail_terminal_failed", task.ID, err)
				result.TransientErrors++
				return
			}
			result.SettledNoProduct++
			return
		case StatusCompleted:
			if _, err := j.Store.AdvanceStatus(ctx, task.ID, task.SystemAccountID, StatusCompleted, formatNow(j.now())); err != nil {
				j.warn("studio 任务完成态推进失败", "studio_task_advance_failed", task.ID, err)
			}
			j.downloadAndSettle(ctx, task, account, affinity, poll.OutputSeconds, VideoDownloadLimit, result)
			return
		}
	}
	result.StillRunning++
}

// settleImage 处理图像任务的结算残留（下载失败残留 / 崩溃于入库前窗口）：
// 按 request_snapshot 的 url 重下载一次，失败落 settlement='failed'（可重试，
// attempt 上限收口）。快照无 url（受理未知）同款结算失败，不自动重放生成。
func (j *SettleJob) settleImage(ctx context.Context, task *TaskRow, result *Result) {
	rawURL := snapshotURL(task.RequestSnapshotJSON)
	if rawURL == "" {
		j.markSettlementFailed(ctx, task, ErrorCodeAcceptanceGone, "任务缺少可重下载的产物地址，请重新发起生成", result)
		return
	}
	data, headerMime, err := j.Upstream.DownloadURL(ctx, rawURL, ImageDownloadLimit)
	if err != nil {
		j.markSettlementFailed(ctx, task, ErrorCodeSettleFailed, "生成图像重下载失败，产物未入库", result)
		return
	}
	// 图像 mime 以魔数识别为准（gateway 图像结算同源口径），响应头仅兜底。
	mimeType := ImageMimeFromBytes(data)
	if mimeType == "" {
		if isImageMime(headerMime) {
			mimeType = headerMime
		} else {
			j.markSettlementFailed(ctx, task, ErrorCodeSettleFailed, "生成图像格式无法识别，产物未入库", result)
			return
		}
	}
	j.writeAndSettle(ctx, task, data, mimeType, nil, ImageDownloadLimit, result)
}

// downloadAndSettle 下载视频产物并走三道闸结算（§5.5 产物落库）。
func (j *SettleJob) downloadAndSettle(ctx context.Context, task *TaskRow, account AccountRuntime, affinity *MediaJobAffinity, outputSeconds *float64, limit int64, result *Result) {
	data, mimeType, err := j.Upstream.DownloadVideo(ctx, account, affinity.UpstreamJobID, limit)
	if err != nil {
		j.markSettlementFailed(ctx, task, ErrorCodeSettleFailed, "生成产物下载失败，产物未入库", result)
		return
	}
	var durationMs *int64
	if outputSeconds != nil && *outputSeconds >= 0 {
		ms := int64(*outputSeconds * 1000)
		durationMs = &ms
	}
	j.writeAndSettle(ctx, task, data, mimeType, durationMs, limit, result)
}

// writeAndSettle 写对象 + 三道闸事务收口（§5.4/§5.5）。对象先写、行与账本
// 同事务提交；收口冲突/配额越界时回收已写对象（不残留占存储的孤儿对象）。
func (j *SettleJob) writeAndSettle(ctx context.Context, task *TaskRow, data []byte, mimeType string, durationMs *int64, limit int64, result *Result) {
	digest := sha256Hex(data)
	assetID := NewAssetID()
	storageKey := StorageKey(assetID, digest, mimeType)
	if err := j.Objects.Write(storageKey, data, limit, digest); err != nil {
		j.markSettlementFailed(ctx, task, ErrorCodeSettleFailed, "生成产物落库失败", result)
		return
	}
	_, reused, err := j.Store.SettleWithAsset(ctx, SettleAssetInput{
		TaskID:  task.ID,
		OwnerID: task.SystemAccountID,
		Asset: AssetInsert{
			ID:           assetID,
			Kind:         task.Kind,
			Mime:         mimeType,
			StorageKey:   storageKey,
			Bytes:        int64(len(data)),
			DurationMs:   durationMs,
			Sha256:       digest,
			SourceTaskID: task.ID,
			ParamsJSON:   orEmptyJSON(task.RequestSnapshotJSON),
		},
		Now: formatNow(j.now()),
	})
	if err != nil {
		_ = j.Objects.Delete([]string{storageKey})
		switch {
		case errors.Is(err, ErrSettleConflict):
			// 迟到成功（取消竞态）/并发收口：任务保持既有态，产物不落库、
			// 不复活任务、账本一致（§4.4/§6）。
			result.Conflicts++
			return
		case errors.Is(err, ErrQuotaExceeded), errors.Is(err, ErrCountExceeded):
			j.markSettlementFailed(ctx, task, ErrorCodeSettleQuota, err.Error(), result)
			return
		default:
			j.warn("studio 任务产物入库失败", "studio_task_settle_tx_failed", task.ID, err)
			j.markSettlementFailed(ctx, task, ErrorCodeSettleFailed, "生成产物入库失败", result)
			return
		}
	}
	result.SettledWithAsset++
	if reused {
		result.ReusedAssets++
	}
}

// videoAffinity 确立视频任务的账户亲和关联（§5.5 崩溃恢复）：
//   - upstream_job_id 已确立 → media_jobs 回读；行缺席（任务面保留清理）→
//     (nil, true, nil)（任务面不可达）；
//   - upstream_job_id 为 NULL（崩溃早于受理持久化）→ 创建关联重扫，命中即
//     采纳其 job id（条件 UPDATE 只采纳一次）；未命中 → (nil, false, nil)
//     （受理未确立）。
func (j *SettleJob) videoAffinity(ctx context.Context, task *TaskRow) (affinity *MediaJobAffinity, gone bool, err error) {
	if task.UpstreamJobID != nil && strings.TrimSpace(*task.UpstreamJobID) != "" {
		loaded, found, err := j.Store.LoadMediaJobAffinity(ctx, strings.TrimSpace(*task.UpstreamJobID))
		if err != nil {
			return nil, false, err
		}
		if !found {
			return nil, true, nil
		}
		return loaded, false, nil
	}
	loaded, found, err := j.Store.FindMediaJobForRescan(ctx, task.SystemAccountID, KindVideo, task.CreatedAt)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	adopted, err := j.Store.AdoptUpstreamJobID(ctx, task.ID, task.SystemAccountID, loaded.UnifiedJobID, formatNow(j.now()))
	if err != nil {
		return nil, false, err
	}
	_ = adopted // 0 行 = gateway 受理回填竞态先到，关联已存在，继续用重扫结果。
	return loaded, false, nil
}

// markSettlementFailed 落结算失败态；条件未命中（并发方已收口 settled）与
// 写失败同按非成功处理（调用方不再计成功计数）。
func (j *SettleJob) markSettlementFailed(ctx context.Context, task *TaskRow, code, message string, result *Result) bool {
	marked, err := j.Store.MarkSettlementFailed(ctx, task.ID, task.SystemAccountID, errorJSON(code, message), formatNow(j.now()))
	if err != nil {
		j.warn("studio 任务结算失败态落定失败", "studio_task_settle_failed_write", task.ID, err)
		result.TransientErrors++
		return false
	}
	if marked {
		result.SettlementFailed++
	}
	return marked
}

func (j *SettleJob) saveBookmark(ctx context.Context, task *TaskRow, bookmark map[string]any) {
	payload, err := json.Marshal(bookmark)
	if err != nil {
		return
	}
	if err := j.Store.SaveBookmark(ctx, task.ID, j.leaseOwner(), string(payload), formatNow(j.now())); err != nil {
		j.warn("studio 任务轮询书签写入失败", "studio_task_bookmark_failed", task.ID, err)
	}
}

// snapshotURL 从 request_snapshot_json 取图像产物重下载地址（{"url": ...}；
// 无/坏值返回空串，不猜测）。
func snapshotURL(snapshotJSON string) string {
	if strings.TrimSpace(snapshotJSON) == "" {
		return ""
	}
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
		return ""
	}
	if raw, ok := snapshot["url"].(string); ok {
		return strings.TrimSpace(raw)
	}
	return ""
}

func isImageMime(mimeType string) bool {
	switch mimeType {
	case "image/png", "image/jpeg", "image/webp":
		return true
	}
	return false
}

// ---- 配置缺省与小工具 ----

func (j *SettleJob) now() time.Time {
	if j.Clock != nil {
		return j.Clock()
	}
	return time.Now()
}

func (j *SettleJob) leaseOwner() string {
	if strings.TrimSpace(j.LeaseOwner) != "" {
		return j.LeaseOwner
	}
	return "juhe-ai-jobs"
}

func (j *SettleJob) leaseTTL() time.Duration {
	if j.LeaseTTL > 0 {
		return j.LeaseTTL
	}
	return LeaseTTL
}

func (j *SettleJob) batchSize() int {
	if j.BatchSize > 0 {
		return j.BatchSize
	}
	return DefaultBatchSize
}

func (j *SettleJob) maxAttempts() int {
	if j.MaxLeaseAttempts > 0 {
		return j.MaxLeaseAttempts
	}
	return MaxLeaseAttempts
}

func (j *SettleJob) maxPolls() int {
	if j.MaxPollsPerClaim > 0 {
		return j.MaxPollsPerClaim
	}
	return DefaultMaxPollsPerClaim
}

func (j *SettleJob) pollEvery() time.Duration {
	if j.PollEvery > 0 {
		return j.PollEvery
	}
	return PollInterval
}

func (j *SettleJob) logger() *slog.Logger {
	if j.Logger != nil {
		return j.Logger
	}
	return slog.Default()
}

func (j *SettleJob) warn(message, event, taskID string, err error) {
	j.logger().Warn(message, "event", event, "taskId", taskID, "error", err.Error())
}

// formatNow 是 RFC3339 毫秒 UTC 文本（gateway studio now() 同 layout，
// TEXT 列字典序 = 时间序）。
func formatNow(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func errorJSON(code, message string) string {
	payload, err := json.Marshal(map[string]string{"code": code, "message": message})
	if err != nil {
		return "{}"
	}
	return string(payload)
}

func orEmptyJSON(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "{}"
	}
	return raw
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// sleepCtx 是可取消的轮询间隔等待。
func sleepCtx(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// NewAssetID 生成 stasset_ 前缀、32 位十六进制随机的资产 id（gateway
// studio.NewAssetID 同构形状）。
func NewAssetID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 失败属系统级故障：id 熵不可降级，显式失败交进程语义
		//（gateway randomHex32 同立场，不静默回退时间熵）。
		panic(fmt.Errorf("studio 资产 id 熵源不可用: %w", err))
	}
	return "stasset_" + hex.EncodeToString(buf)
}
