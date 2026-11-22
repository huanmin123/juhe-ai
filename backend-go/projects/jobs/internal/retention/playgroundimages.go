package retention

// playground-images-retention（演练场设计 §3.4.5 保留期与删除语义 / §3.8
// 产物暂存 / §5.1 schema）：演练场图像工作台的产物字节保留清理。Go 新增
// 任务（归档 Node 无对应 scheduled job，演练场域 v1.2 新表）。语义：
//   - 阶段一（按任务清理，两类目标行统一执行「删字节 → 置位」）：扫
//     playground_image_jobs 中未置位的图像资源过期任务行——
//       1. completed 图像到期：expires_at>0 且 expires_at<now（走
//          idx_playground_image_jobs_expiry 部分索引）；
//       2. failed 参考图超窗：status='failed' 且 updated_at<now-7d（failed
//          任务无结果图、expires_at=0，但创建事务绑定的 image_reference
//          参考图行仍占存储；窗口 7d 与图像保留期同值，自任务最后更新
//          时间起算——§3.8 边界「图像存储总量由到期清理自然封顶」对
//          failed 任务的补全）。
//     对每行按 (system_account_id, job_id) 删除 playground_artifacts 的
//     image_result+image_reference 行（结果图与参考图一起删；failed 行
//     实际只有参考图），随后置任务行 images_expired=true。images_expired
//     语义为「图像资源已清理」标记，两类目标行共用（completed 图像到期 /
//     failed 参考图超窗）；到期清理不删任务记录行（记录行仅经演练场设计
//     §3.4.6 v1.5 手动删除入口移除），卡片按 §3.4.5 显示「已删除
//     图像」与历史参数；UPDATE 带未置位守卫，并发重复处理时置位只生效
//     一次（幂等防竞态），删除天然幂等；
//   - 阶段二（退役遗留与孤儿，各一条批量语句）：v1.0 覆盖式暂存退役遗留
//     kind='image' 按 created_at 7d 窗口顺带删除；孤儿参考图（image_reference
//     且 job_id=''，上传后未绑定任务）按 24h 窗口清理（与 gateway 懒清理
//     同窗口，双保险）；
//   - 分批限流（默认每批 500 行 × 单轮最多 20 批）+ 表缺席快速返回。
//
// 时间列均为 UnixMilli bigint（gateway 侧 playground_artifacts 写入
// time.Now().UnixMilli()，expires_at = completed_at + 7d，演练场设计
// §3.4.2）；过期清理不产生计费（演练场设计 §3.7）。
//
// 表定义在 maintenance schema（pg_schema_business_tables.go），jobs 侧只
// 读写不建表；表缺席（老库未跑 maintenance 迁移）视为零任务快速返回，
// 不报错。

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// PlaygroundImagesRetentionJob 是 playground-images-retention 的任务体
// （组合根经 scheduleWiredJob 装配；DB 缺席显式报错不静默）。
type PlaygroundImagesRetentionJob struct {
	// Store 是演练场图像产物清理仓储；nil 显式报错。
	Store PlaygroundImagesStore
	// Clock 注入时间源（测试可固定）。
	Clock func() time.Time
	// Logger 缺省 slog.Default()。
	Logger *slog.Logger
	// BatchSize 每批扫描任务行上限（缺省 playgroundImagesRetentionBatchSize）。
	BatchSize int
}

// PlaygroundImageJobRef 是一条图像资源待清理任务行的定位引用（两类目标行
// 共用：completed 到期 / failed 参考图超窗；playground_image_jobs 主键
// (system_account_id, id)）。
type PlaygroundImageJobRef struct {
	SystemAccountID string
	JobID           string
}

// PlaygroundImagesStore 是演练场图像产物清理仓储端口（PG 方言 $n 绑定，
// 与 gatewayplayground 仓储同表同键约定——jobs 项目不 import gateway 模块，
// 表名/kind 词表在此镜像）。
type PlaygroundImagesStore interface {
	// ListExpiredJobs 返回一批未置位的图像资源待清理任务行（≤limit 行）：
	// completed 到期（expires_at>0 且 images_expired=false 且
	// expires_at<nowMS）与 failed 参考图超窗（status='failed' 且
	// images_expired=false 且 updated_at<failedBeforeMS）两类目标的并集。
	ListExpiredJobs(ctx context.Context, nowMS int64, failedBeforeMS int64, limit int) ([]PlaygroundImageJobRef, error)
	// DeleteJobArtifacts 删除一个任务的结果图与参考图字节行
	//（kind IN ('image_result','image_reference')），返回删除行数。
	DeleteJobArtifacts(ctx context.Context, systemAccountID, jobID string) (int64, error)
	// MarkJobImagesExpired 把任务行置 images_expired=true 并更新 updated_at
	//（带 images_expired=false 守卫，并发先置者生效），返回置位行数。
	MarkJobImagesExpired(ctx context.Context, systemAccountID, jobID string, nowMS int64) (int64, error)
	// DeleteLegacyImagesBefore 删除 v1.0 覆盖式暂存退役遗留（kind='image'，
	// created_at 早于 createdBeforeMS），返回删除行数。
	DeleteLegacyImagesBefore(ctx context.Context, createdBeforeMS int64) (int64, error)
	// DeleteOrphanReferencesBefore 删除孤儿参考图（kind='image_reference'
	// 且 job_id=''，created_at 早于 createdBeforeMS），返回删除行数。
	DeleteOrphanReferencesBefore(ctx context.Context, createdBeforeMS int64) (int64, error)
}

// playgroundImagesRetentionBatchSize 是每批扫描任务行上限（任务族惯例
// 量级：每用户 7 天窗口封顶的单任务产物集，行量级远低于 usage records）。
const playgroundImagesRetentionBatchSize = 500

// playgroundImagesRetentionMaxBatchesPerRun 是单次 Run 的最大批次（与
// media-jobs-retention 同款：积压时单轮最多 20 批清 1 万行，仍受调度器
// 2 分钟 timeout 约束；正常零/少量到期行时第一批即退出）。
const playgroundImagesRetentionMaxBatchesPerRun = 20

// PlaygroundImageArtifactsTTL 与 gateway 演练场图像保留口径同值（演练场
// 设计 §3.4.2：expires_at = completed_at + 7d；§3.8：结果图与参考图一并
// 保留 7 天）；两侧常量镜像，jobs 不 import gateway 模块。同值复用为
// failed 任务参考图的保留窗口（自任务 updated_at 起算，见阶段一目标行 2）。
const PlaygroundImageArtifactsTTL = 7 * 24 * time.Hour

// playgroundOrphanReferenceTTL 是孤儿参考图（job_id=”）的保留窗口
// （演练场设计 §3.8：上传后 24h 内未绑定任务即删；gateway 懒清理同窗口，
// 本任务批量语句为兜底）。
const playgroundOrphanReferenceTTL = 24 * time.Hour

// Run 执行一轮清理：阶段一逐行删字节 + 置位，阶段二清退役遗留与孤儿。
// 表缺席（老库未跑 maintenance 迁移）按零任务快速返回（Info 告知一次语义
// 由调用方日志承担）。
func (j *PlaygroundImagesRetentionJob) Run(ctx context.Context) (PlaygroundImagesRetentionResult, error) {
	if j.Store == nil {
		return PlaygroundImagesRetentionResult{}, fmt.Errorf("retention playground images store 未初始化")
	}
	batchSize := j.BatchSize
	if batchSize <= 0 {
		batchSize = playgroundImagesRetentionBatchSize
	}
	clock := j.Clock
	if clock == nil {
		clock = time.Now
	}
	nowMS := clock().UnixMilli()
	// failed 参考图保留窗边界：7d 与图像保留期同值，自任务最后更新时间起算。
	failedBeforeMS := nowMS - PlaygroundImageArtifactsTTL.Milliseconds()
	result := PlaygroundImagesRetentionResult{}
	// 阶段一：分批扫描待清理任务（completed 到期 + failed 参考图超窗两类
	// 目标行）清到空或达到单次上限。扫描行集只含未置位行，处理过的行本轮
	// 即退出扫描集，下一批自然覆盖剩余行。
	for batch := 0; batch < playgroundImagesRetentionMaxBatchesPerRun; batch++ {
		if ctx.Err() != nil {
			break
		}
		jobs, err := j.Store.ListExpiredJobs(ctx, nowMS, failedBeforeMS, batchSize)
		if err != nil {
			if IsPlaygroundTablesMissing(err) {
				return result, nil
			}
			return result, fmt.Errorf("扫描过期演练场图像任务失败: %w", err)
		}
		for _, ref := range jobs {
			// 先删字节再置位：两步之间中断时任务行仍未置位，下一轮重扫
			// 重删（删除幂等，0 行）后置位，字节与标记最终一致。
			deleted, err := j.Store.DeleteJobArtifacts(ctx, ref.SystemAccountID, ref.JobID)
			if err != nil {
				if IsPlaygroundTablesMissing(err) {
					return result, nil
				}
				return result, fmt.Errorf("删除演练场图像任务产物失败: %w", err)
			}
			marked, err := j.Store.MarkJobImagesExpired(ctx, ref.SystemAccountID, ref.JobID, nowMS)
			if err != nil {
				if IsPlaygroundTablesMissing(err) {
					return result, nil
				}
				return result, fmt.Errorf("置位演练场图像任务过期标记失败: %w", err)
			}
			result.DeletedArtifacts += deleted
			result.ExpiredJobs += marked
		}
		result.Batches = batch + 1
		if len(jobs) < batchSize {
			break
		}
	}
	// 阶段二：退役遗留与孤儿参考图各一条批量语句（每轮一次，不在分批
	// 循环内重复执行）。
	legacyBefore := nowMS - PlaygroundImageArtifactsTTL.Milliseconds()
	deletedLegacy, err := j.Store.DeleteLegacyImagesBefore(ctx, legacyBefore)
	if err != nil {
		if IsPlaygroundTablesMissing(err) {
			return result, nil
		}
		return result, fmt.Errorf("删除演练场 v1.0 遗留图像失败: %w", err)
	}
	result.DeletedLegacyImages = deletedLegacy
	orphanBefore := nowMS - playgroundOrphanReferenceTTL.Milliseconds()
	deletedOrphan, err := j.Store.DeleteOrphanReferencesBefore(ctx, orphanBefore)
	if err != nil {
		if IsPlaygroundTablesMissing(err) {
			return result, nil
		}
		return result, fmt.Errorf("删除演练场孤儿参考图失败: %w", err)
	}
	result.DeletedOrphanReferences = deletedOrphan
	if result.ExpiredJobs > 0 || result.DeletedArtifacts > 0 || result.DeletedLegacyImages > 0 || result.DeletedOrphanReferences > 0 {
		j.logger().Info("演练场图像产物保留清理完成",
			"event", "playground_images_retention_cleanup",
			"expiredJobs", result.ExpiredJobs,
			"deletedArtifacts", result.DeletedArtifacts,
			"deletedLegacyImages", result.DeletedLegacyImages,
			"deletedOrphanReferences", result.DeletedOrphanReferences,
			"batches", result.Batches,
			"batchSize", batchSize,
			"retentionTTL", PlaygroundImageArtifactsTTL.String())
	}
	return result, nil
}

func (j *PlaygroundImagesRetentionJob) logger() *slog.Logger {
	if j.Logger != nil {
		return j.Logger
	}
	return slog.Default()
}

// PlaygroundImagesRetentionResult 是单轮清理计数。
type PlaygroundImagesRetentionResult struct {
	// ExpiredJobs 是阶段一置位 images_expired（图像资源已清理）的任务行数
	//（两类目标行共用计数；并发已被置位的行不计入）。
	ExpiredJobs int64
	// DeletedArtifacts 是阶段一删除的结果图/参考图字节行数（failed 行实际
	// 只删参考图）。
	DeletedArtifacts int64
	// DeletedLegacyImages 是阶段二删除的 v1.0 覆盖式暂存遗留行数。
	DeletedLegacyImages int64
	// DeletedOrphanReferences 是阶段二删除的孤儿参考图行数。
	DeletedOrphanReferences int64
	// Batches 是本次 Run 实际执行的分批轮数（上限
	// playgroundImagesRetentionMaxBatchesPerRun）。
	Batches int
}

// ---- SQL 实现（组合根供 worker_retention.go 装配） ----

// PlaygroundImagesSQLStore 是演练场图像产物清理仓储的 PG SQL 实现
// （$n 方言绑定；playground 表族含 bytea/jsonb 列、仅存在于 PG 业务库
// schema，无 sqlite 分支。表缺席按零任务处理不报错）。
type PlaygroundImagesSQLStore struct {
	DB *sql.DB
}

// ListExpiredJobs 扫两类未置位目标行的并集（≤limit 行）：
//   - 到期臂：expires_at>0 AND expires_at<now AND images_expired=false
//     （前两个条件命中 idx_playground_image_jobs_expiry 部分索引
//     WHERE expires_at > 0）；
//   - failed 臂：status='failed' AND updated_at<failedBefore AND
//     images_expired=false（表无 status/updated_at 索引）。
//
// 两条扫描而非一条 OR：failed 臂无索引路径，合并 OR 后 planner 无法
// BitmapOr，整条谓词退化为 Seq Scan+Filter，反而破坏到期臂既有的部分索引
// 利用（到期清理不删任务记录行，表随时间增长）。故先索引扫描到期臂，不足
// 一批才补扫 failed 臂（每轮至多一次顺序过滤，是该臂无索引下的最小代价；
// 为 failed 臂建索引属 maintenance schema 变更，不在 jobs 侧范围）。两臂
// 结果按键去重取并集（正常写入约定 failed 行 expires_at=0 不与到期臂
// 重叠，去重只落实并集语义）。
func (s *PlaygroundImagesSQLStore) ListExpiredJobs(ctx context.Context, nowMS int64, failedBeforeMS int64, limit int) ([]PlaygroundImageJobRef, error) {
	var refs []PlaygroundImageJobRef
	seen := make(map[string]bool, limit)
	rows, err := s.DB.QueryContext(ctx,
		`SELECT system_account_id, id FROM juhe_business.playground_image_jobs
		 WHERE expires_at > 0 AND expires_at < $1 AND images_expired = false
		 LIMIT $2`, nowMS, limit)
	if err != nil {
		return nil, err
	}
	refs, err = collectPlaygroundJobRefs(rows, refs, seen, limit)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(refs) >= limit {
		return refs, nil
	}
	rows, err = s.DB.QueryContext(ctx,
		`SELECT system_account_id, id FROM juhe_business.playground_image_jobs
		 WHERE status = 'failed' AND images_expired = false AND updated_at < $1
		 LIMIT $2`, failedBeforeMS, limit-len(refs))
	if err != nil {
		return nil, err
	}
	refs, err = collectPlaygroundJobRefs(rows, refs, seen, limit)
	rows.Close()
	if err != nil {
		return nil, err
	}
	return refs, nil
}

// collectPlaygroundJobRefs 把一条扫描的行并入 refs（按键去重，≤limit 封顶）。
func collectPlaygroundJobRefs(rows *sql.Rows, refs []PlaygroundImageJobRef, seen map[string]bool, limit int) ([]PlaygroundImageJobRef, error) {
	for rows.Next() {
		var ref PlaygroundImageJobRef
		if err := rows.Scan(&ref.SystemAccountID, &ref.JobID); err != nil {
			return refs, err
		}
		key := ref.SystemAccountID + "|" + ref.JobID
		if seen[key] {
			continue
		}
		seen[key] = true
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

// DeleteJobArtifacts 删除一个任务的结果图与参考图字节（结果图与参考图
// 一起删，演练场设计 §3.8）。
func (s *PlaygroundImagesSQLStore) DeleteJobArtifacts(ctx context.Context, systemAccountID, jobID string) (int64, error) {
	result, err := s.DB.ExecContext(ctx,
		`DELETE FROM juhe_business.playground_artifacts
		 WHERE system_account_id = $1 AND job_id = $2 AND kind IN ('image_result','image_reference')`,
		systemAccountID, jobID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// MarkJobImagesExpired 置位 images_expired（「图像资源已清理」标记，
// completed 到期与 failed 参考图超窗两类目标行共用；未置位守卫：扫描快照
// 与行锁之间并发的重复处理只生效一次，updated_at 以先置时刻为准）。
func (s *PlaygroundImagesSQLStore) MarkJobImagesExpired(ctx context.Context, systemAccountID, jobID string, nowMS int64) (int64, error) {
	result, err := s.DB.ExecContext(ctx,
		`UPDATE juhe_business.playground_image_jobs
		 SET images_expired = true, updated_at = $3
		 WHERE system_account_id = $1 AND id = $2 AND images_expired = false`,
		systemAccountID, jobID, nowMS)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// DeleteLegacyImagesBefore 删除 v1.0 覆盖式暂存退役遗留（kind='image'
// 按 7d 窗口，演练场设计 §3.8「存量行由 jobs 清理任务按 7d 窗口顺带删除」）。
func (s *PlaygroundImagesSQLStore) DeleteLegacyImagesBefore(ctx context.Context, createdBeforeMS int64) (int64, error) {
	result, err := s.DB.ExecContext(ctx,
		`DELETE FROM juhe_business.playground_artifacts
		 WHERE kind = 'image' AND created_at < $1`, createdBeforeMS)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// DeleteOrphanReferencesBefore 删除孤儿参考图（job_id=” 按 24h 窗口）。
func (s *PlaygroundImagesSQLStore) DeleteOrphanReferencesBefore(ctx context.Context, createdBeforeMS int64) (int64, error) {
	result, err := s.DB.ExecContext(ctx,
		`DELETE FROM juhe_business.playground_artifacts
		 WHERE kind = 'image_reference' AND job_id = '' AND created_at < $1`, createdBeforeMS)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// IsPlaygroundTablesMissing 报告错误是否为 playground 表缺席（老库零任务
// 快速返回的判据；错误文本判 relation does not exist，与
// IsMediaJobsTableMissing 同模式——两表任一缺席即整任务零返回）。
func IsPlaygroundTablesMissing(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, `playground_image_jobs" does not exist`) ||
		strings.Contains(message, "playground_image_jobs does not exist") ||
		strings.Contains(message, `playground_artifacts" does not exist`) ||
		strings.Contains(message, "playground_artifacts does not exist")
}
