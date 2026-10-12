package mockdata

// 媒体工作室样本域（媒体工作室设计 §9）：studio_assets / studio_tasks /
// studio_operations / studio_user_asset_usage 样本与 purpose=studio Key
//（EnsureStudioKey 同构落行），主数据归属 seed 超管 admin（「身份归属」惯例）。
//
// 与契约 §9 的对应关系（契约是唯一权威，覆盖任务书的举例条数）：
//   - 资产 4 行：image（宽高 + params_json 生成参数快照 + source_task_id 回填）、
//     audio（TTS 产物，params_json 含 voice/speed 摘要）、video（含 duration_ms）、
//     另有 1 行上传来源行（source_task_id IS NULL）；
//   - 任务 9 行：契约 §9 列出的八种 status × settlement 关键组合各 1 行，外加
//     1 行 video 的 completed+settled（video 资产的 source_task_id 回填载体，
//     同时让亲和三元组 + upstream_job_id 指向既有 media_jobs 样本行）；
//   - 收据 2 行：studio_operations 各 kind ≥1（speech 带 asset_id，
//     transcription 带 result_json 文本）；
//   - 账本 1 行：studio_user_asset_usage 按资产样本行求和（配额账本一致）。
//
// 对外 id 沿媒体工作室表注释契约前缀（stasset_/stask_/stoper_）+ mockdata
// 标识段，清理经 cleanupRules 的 id 前缀定位（media_jobs 的 video_ 前缀同款
// 裁决：契约前缀行不带 CleanupIDPrefix 开头，通用标识扫描兜不到，必须显式
// 登记清理规则）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DomainStudio 是媒体工作室样本域的注册键、覆盖断言归属键与报告域名。
const DomainStudio = "studio"

// seedStudio 写入媒体工作室样本域。business 域未接线（缺 schema/seed）或
// studio 表未建（旧版 schema）时跳过并留空 Counts：覆盖报告按「未接线」呈现，
// 不让整个造数失败（seedBusiness 守卫同款语义）。
func seedStudio(ctx context.Context, e *env) (DomainResult, error) {
	guard, err := resolveBusinessGuard(ctx, e)
	if err != nil {
		return DomainResult{}, err
	}
	if guard.skip != "" {
		e.logger.Info("mockdata studio 域跳过", "reason", guard.skip)
		return DomainResult{Name: DomainStudio}, nil
	}
	db, err := e.openExisting(StoreBusiness)
	if err != nil {
		return DomainResult{}, err
	}
	for _, table := range []string{"studio_assets", "studio_tasks", "studio_operations", "studio_user_asset_usage"} {
		present, tableErr := queryExistsTable(ctx, db, table)
		if tableErr != nil {
			return DomainResult{}, tableErr
		}
		if !present {
			e.logger.Info("mockdata studio 域跳过", "reason", "business 库缺少表 "+table+"（schema 未含媒体工作室批次）")
			return DomainResult{Name: DomainStudio}, nil
		}
	}

	writer := &businessWriter{
		ctx:    ctx,
		now:    e.options.clock(),
		secret: businessRuntimeSecret(e.options.Secret),
		counts: map[string]int{},
	}
	if err := e.tx(ctx, StoreBusiness, func(tx *sql.Tx) error {
		// 计数语义与域内 put() 一致：本次运行确保存在即计 1（插入或复用）。
		if err := writer.ensureStudioAPIKey(tx, guard); err != nil {
			return err
		}
		writer.counts["api_keys"]++
		if err := writer.seedStudioAssets(tx, guard); err != nil {
			return err
		}
		if err := writer.seedStudioTasks(tx, guard); err != nil {
			return err
		}
		if err := writer.seedStudioOperations(tx, guard); err != nil {
			return err
		}
		return writer.seedStudioUsageLedger(tx, guard)
	}); err != nil {
		return DomainResult{}, err
	}

	for _, key := range writer.apiKeys {
		e.addAPIKey(key)
	}
	return DomainResult{Name: DomainStudio, Counts: writer.counts}, nil
}

// studioKeyPlaintext 是 studio 样本 Key 的确定性明文（摘要登记后供本地网关
// 联调 /v1 链使用）；key_hash / key_secret_encrypted 派生方式与
// seedApiKeys 的 general Key 一致。
func studioKeyPlaintext() string {
	return "sk-" + CleanupTracePrefix + "studio-" + strings.Repeat("s", 24)
}

// ensureStudioAPIKey 按 EnsureStudioKey 同构落行（媒体工作室设计 §3.5）：
// 探测 (system_account_id, purpose='studio') 现存 Key → 存在即复用 → 不存在
// 则插入新行（purpose='studio' + studio_domains_json 四域 + 专属路由策略）。
// 与 business 域 put() 的 INSERT OR REPLACE 不同，这里用普通 INSERT ON
// CONFLICT DO NOTHING：存量库可能已有 gateway 运行时 EnsureStudioKey 建的
// 真实 studio Key，撞 idx_api_keys_studio_purpose_unique 时必须保留胜者行，
// 不得 REPLACE 删除。
func (w *businessWriter) ensureStudioAPIKey(tx *sql.Tx, guard businessGuard) error {
	var existingID string
	err := tx.QueryRowContext(w.ctx,
		"SELECT id FROM api_keys WHERE system_account_id = ? AND purpose = 'studio' LIMIT 1",
		guard.adminID).Scan(&existingID)
	if err == nil {
		w.registerStudioAPIKey(existingID, guard)
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("探测 studio Key: %w", err)
	}

	// 专属路由策略（EnsureStudioKey 绑定默认策略路由的同构样本）：确定性 id
	// 重复执行幂等；分组绑定取 admin 名下第一个分组（无分组时跳过绑定，
	// preauth 链结构样本不因此缺席）。
	strategyID := CleanupIDPrefix + "route_studio"
	now := w.stamp()
	if err := w.put(tx, "route_strategies", map[string]any{
		"id": strategyID, "system_account_id": guard.adminID,
		"name":        CleanupNamePrefix + "媒体工作室 API Key 策略",
		"description": "Mockdata 造数生成的媒体工作室策略路由（EnsureStudioKey 同构样本），模式 normal",
		"mode":        "normal", "status": "active", "is_default": 0,
		"config_json": nil, "created_at": now, "updated_at": now,
	}); err != nil {
		return err
	}
	var groupID string
	if err := tx.QueryRowContext(w.ctx,
		"SELECT id FROM groups WHERE system_account_id = ? ORDER BY is_default DESC, id LIMIT 1",
		guard.adminID).Scan(&groupID); err == nil {
		if err := w.put(tx, "route_strategy_groups", map[string]any{
			"id": CleanupIDPrefix + "rsg_studio", "route_strategy_id": strategyID,
			"system_account_id": guard.adminID, "group_id": groupID,
			"priority": 1, "weight": 1, "status": "active",
			"created_at": now, "updated_at": now,
		}); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("读取 studio 策略绑定分组: %w", err)
	}

	plaintext := studioKeyPlaintext()
	sealed, err := w.seal(map[string]any{"key": plaintext})
	if err != nil {
		return err
	}
	domainsJSON, err := w.putJSON([]string{"text", "image", "audio", "video"})
	if err != nil {
		return err
	}
	// ON CONFLICT 不带冲突目标：命中任何唯一约束（含
	// idx_api_keys_studio_purpose_unique partial index）都保留既有行。
	result, err := tx.ExecContext(w.ctx, `INSERT INTO api_keys (
      id, system_account_id, route_strategy_id, name, description, key_hash,
      key_prefix, key_suffix, key_secret_encrypted, status, is_default, purpose,
      studio_domains_json, expires_at, quota_limits_json, availability_schedule_json,
      availability_schedule_next_check_at, last_used_at, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', 0, 'studio', ?, NULL, NULL, NULL, NULL, NULL, ?, ?)
    ON CONFLICT DO NOTHING`,
		CleanupIDPrefix+"key_studio", guard.adminID, strategyID,
		CleanupNamePrefix+"媒体工作室 API Key",
		"Mockdata 媒体工作室专用 Key（EnsureStudioKey 同构样本，域授权恒四域）",
		secretHash(plaintext), plaintext[:8], plaintext[len(plaintext)-8:], sealed,
		domainsJSON, now, now)
	if err != nil {
		return fmt.Errorf("写入 studio Key: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	keyID := CleanupIDPrefix + "key_studio"
	if inserted == 0 {
		// 并发/重复执行撞唯一索引：重探复用胜者行（§3.5 幂等机制）。
		if err := tx.QueryRowContext(w.ctx,
			"SELECT id FROM api_keys WHERE system_account_id = ? AND purpose = 'studio' LIMIT 1",
			guard.adminID).Scan(&keyID); err != nil {
			return fmt.Errorf("重探 studio Key: %w", err)
		}
	}
	w.registerStudioAPIKey(keyID, guard)
	return nil
}

// registerStudioAPIKey 把 studio Key 登记进摘要（本地联调 /v1 链的明文出口）。
func (w *businessWriter) registerStudioAPIKey(keyID string, guard businessGuard) {
	w.apiKeys = append(w.apiKeys, mockAPIKey{
		Name:                 CleanupNamePrefix + "媒体工作室 API Key",
		ID:                   keyID,
		Label:                "studio",
		Key:                  studioKeyPlaintext(),
		OwnerSystemAccountID: guard.adminID, OwnerSystemAccountName: guard.adminDisplayName,
		RouteStrategyID:   CleanupIDPrefix + "route_studio",
		RouteStrategyName: CleanupNamePrefix + "媒体工作室 API Key 策略",
		RouteStrategyMode: "normal", RouteStrategyStatus: "active", Status: "active",
	})
}

// studioUpstream 是任务样本的受理引用：upstream_job_id 指向既有 media_jobs
// 样本行（§9），亲和三元组与该行一致；media_jobs 样本行缺席的老库回落
// media 账户行并留空 job id（§5.5「崩溃早于受理持久化」的自洽语义）。
type studioUpstream struct {
	jobID        string
	accountID    string
	providerCode string
}

// resolveStudioUpstream 按 media_jobs 样本行解析受理引用。
func resolveStudioUpstream(ctx context.Context, tx *sql.Tx, mediaJobID string) (studioUpstream, error) {
	var upstream studioUpstream
	err := tx.QueryRowContext(ctx,
		"SELECT id, account_id, provider_code FROM media_jobs WHERE id = ?", mediaJobID).
		Scan(&upstream.jobID, &upstream.accountID, &upstream.providerCode)
	if err == nil {
		return upstream, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return studioUpstream{}, fmt.Errorf("读取 media_jobs 样本行 %s: %w", mediaJobID, err)
	}
	// 回落 media 账户行（老库无该样本行时保证亲和三元组仍指向真实存在的行）。
	fallback := studioUpstream{}
	if err := tx.QueryRowContext(ctx,
		"SELECT id, provider_code FROM accounts WHERE id = ?", CleanupIDPrefix+"acc_media").
		Scan(&fallback.accountID, &fallback.providerCode); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return studioUpstream{}, nil
		}
		return studioUpstream{}, fmt.Errorf("读取媒体账户回落行: %w", err)
	}
	return fallback, nil
}

// studioAssetSeed 是一条资产样本。
type studioAssetSeed struct {
	suffix       string
	kind         string
	mime         string
	storageKey   string
	bytes        int64
	width        any
	height       any
	durationMs   any
	sourceTaskID any
}

// seedStudioAssets 写入资产样本 4 行（契约 §9：三 kind 各 1 行 + 1 行上传
// 来源行）。
func (w *businessWriter) seedStudioAssets(tx *sql.Tx, guard businessGuard) error {
	paramsBySuffix := map[string]string{}
	for suffix, params := range map[string]map[string]any{
		"image":  {"model": guard.catalog.imageModelAt(0), "prompt": CleanupNamePrefix + "像素日落海报", "size": "1024x1024"},
		"audio":  {"source": "speech", "voice": "alloy", "speed": 1.0},
		"video":  {"model": guard.catalog.videoModelAt(0), "prompt": CleanupNamePrefix + "城市延时镜头", "seconds": 4, "size": "1280x720"},
		"upload": {"source": "upload", "filename": CleanupNamePrefix + "参考图.png"},
	} {
		encoded, err := w.putJSON(params)
		if err != nil {
			return err
		}
		paramsBySuffix[suffix] = encoded
	}
	seeds := []studioAssetSeed{
		{
			suffix: "image", kind: "image", mime: "image/png",
			storageKey: "studio/generations/mockdata-image.png", bytes: 1310720,
			width: 1024, height: 1024, durationMs: nil,
			sourceTaskID: "stask_mockdata_image_completed",
		},
		{
			suffix: "audio", kind: "audio", mime: "audio/mpeg",
			storageKey: "studio/speech/mockdata-audio.mp3", bytes: 917504,
			width: nil, height: nil,
			durationMs:   nil, // 时长来源缺失保持 NULL（契约 §3.1）
			sourceTaskID: nil, // TTS 同步端点产物无任务行（契约 §3.3）
		},
		{
			suffix: "video", kind: "video", mime: "video/mp4",
			storageKey: "studio/generations/mockdata-video.mp4", bytes: 4194304,
			width: 1280, height: 720, durationMs: 4000,
			sourceTaskID: "stask_mockdata_video_completed",
		},
		{
			suffix: "upload", kind: "image", mime: "image/png",
			storageKey: "studio/uploads/mockdata-reference.png", bytes: 262144,
			width: 512, height: 512, durationMs: nil,
			sourceTaskID: nil, // 上传来源行（契约 §9 第 4 行）
		},
	}
	for _, seed := range seeds {
		if err := w.put(tx, "studio_assets", map[string]any{
			"id": "stasset_mockdata_" + seed.suffix, "system_account_id": guard.adminID,
			"kind": seed.kind, "mime": seed.mime, "storage_key": seed.storageKey,
			"bytes": seed.bytes, "width": seed.width, "height": seed.height,
			"duration_ms":    seed.durationMs,
			"sha256":         secretHash("studio-asset-" + seed.suffix),
			"source_task_id": seed.sourceTaskID, "params_json": paramsBySuffix[seed.suffix],
			"created_at": w.at(-48 * time.Hour), "updated_at": w.at(-47 * time.Hour),
		}); err != nil {
			return err
		}
	}
	return nil
}

// studioTaskSeed 是一条任务样本。
type studioTaskSeed struct {
	suffix     string
	kind       string
	status     string
	settlement string
	// upstreamMediaJobID 非空时亲和三元组与 upstream_job_id 指向该
	// media_jobs 样本行；空字符串表示受理前样本（三元组留空）。
	upstreamMediaJobID string
	assetID            any
	error              map[string]any
	leaseAttemptCount  int
	ageHours           int
}

// seedStudioTasks 写入任务样本 9 行（契约 §9 八种组合 + video completed+settled
// 载体行），覆盖状态 × 结算正交。
func (w *businessWriter) seedStudioTasks(tx *sql.Tx, guard businessGuard) error {
	seeds := []studioTaskSeed{
		{suffix: "queued", kind: "video", status: "queued", settlement: "pending_settled", ageHours: 1},
		{suffix: "in_progress", kind: "video", status: "in_progress", settlement: "pending_settled",
			upstreamMediaJobID: "video_mockdata_running", ageHours: 2},
		{suffix: "image_completed", kind: "image", status: "completed", settlement: "settled",
			assetID: "stasset_mockdata_image", ageHours: 30},
		{suffix: "video_completed", kind: "video", status: "completed", settlement: "settled",
			upstreamMediaJobID: "video_mockdata_completed", assetID: "stasset_mockdata_video", ageHours: 26},
		{suffix: "failed", kind: "video", status: "failed", settlement: "settled",
			error: map[string]any{"code": "studio_dispatch_exhausted",
				"message": CleanupNamePrefix + "受理前派发穷尽（无产物可结算，settled 收口）"}, ageHours: 20},
		{suffix: "cancelled", kind: "video", status: "cancelled", settlement: "settled", ageHours: 40},
		{suffix: "expired", kind: "video", status: "expired", settlement: "settled",
			upstreamMediaJobID: "video_mockdata_expired", ageHours: 60},
		{suffix: "video_pending_settle", kind: "video", status: "completed", settlement: "pending_settled",
			upstreamMediaJobID: "video_mockdata_completed", assetID: nil, ageHours: 3},
		{suffix: "video_settle_failed", kind: "video", status: "completed", settlement: "failed",
			upstreamMediaJobID: "video_mockdata_completed", assetID: nil, leaseAttemptCount: 5, ageHours: 22},
	}
	for _, seed := range seeds {
		upstream := studioUpstream{}
		if seed.upstreamMediaJobID != "" {
			resolved, err := resolveStudioUpstream(w.ctx, tx, seed.upstreamMediaJobID)
			if err != nil {
				return err
			}
			upstream = resolved
		}
		requestSnapshot, err := w.putJSON(studioTaskRequestSnapshot(seed, guard))
		if err != nil {
			return err
		}
		taskError := seed.error
		if taskError == nil && seed.settlement == "failed" {
			taskError = map[string]any{"code": "studio_settle_failed",
				"message": CleanupNamePrefix + "产物下载/入库失败达到重试上限（终局 failed）"}
		}
		if taskError == nil {
			taskError = map[string]any{}
		}
		errorJSON, err := w.putJSON(taskError)
		if err != nil {
			return err
		}
		pollBookmark := map[string]any{}
		if seed.status == "in_progress" {
			pollBookmark = map[string]any{"upstreamStatus": "in_progress"}
		}
		pollJSON, err := w.putJSON(pollBookmark)
		if err != nil {
			return err
		}
		createdAt := w.at(-time.Duration(seed.ageHours) * time.Hour)
		updatedAt := w.at(-time.Duration(seed.ageHours-1) * time.Hour)
		if err := w.put(tx, "studio_tasks", map[string]any{
			"id": "stask_mockdata_" + seed.suffix, "system_account_id": guard.adminID,
			"client_operation_id": "mockdata-op-" + seed.suffix,
			"kind":                seed.kind, "status": seed.status, "settlement": seed.settlement,
			"account_id":            nullString(upstream.accountID),
			"provider_code":         nullString(upstream.providerCode),
			"upstream_job_id":       nullString(upstream.jobID),
			"request_snapshot_json": requestSnapshot, "error_json": errorJSON,
			"asset_id": seed.assetID, "poll_bookmark_json": pollJSON,
			"lease_owner": nil, "lease_expires_at": nil,
			"lease_attempt_count": seed.leaseAttemptCount,
			"created_at":          createdAt, "updated_at": updatedAt,
		}); err != nil {
			return err
		}
	}
	return nil
}

// studioTaskRequestSnapshot 生成任务请求快照（轻量参数，零存储同款约束：不含
// 资源字节）。
func studioTaskRequestSnapshot(seed studioTaskSeed, guard businessGuard) map[string]any {
	if seed.kind == "image" {
		return map[string]any{
			"model": guard.catalog.imageModelAt(0), "prompt": CleanupNamePrefix + "像素日落海报",
			"size": "1024x1024",
		}
	}
	return map[string]any{
		"model": guard.catalog.videoModelAt(0), "prompt": CleanupNamePrefix + "城市延时镜头",
		"seconds": 4, "size": "1280x720",
	}
}

// seedStudioOperations 写入同步生成幂等收据样本（契约 §9：各 kind ≥1 行）。
func (w *businessWriter) seedStudioOperations(tx *sql.Tx, guard businessGuard) error {
	transcriptionResult, err := w.putJSON(map[string]any{
		"text": CleanupNamePrefix + "转写文本样本：媒体工作室语音转文字链路验收。",
		"srt":  "1\n00:00:00,000 --> 00:00:02,000\n" + CleanupNamePrefix + "转写文本样本\n",
	})
	if err != nil {
		return err
	}
	seeds := []struct {
		suffix     string
		kind       string
		assetID    any
		resultJSON string
	}{
		{suffix: "speech", kind: "audio_speech", assetID: "stasset_mockdata_audio", resultJSON: "{}"},
		{suffix: "transcription", kind: "audio_transcription", assetID: nil, resultJSON: transcriptionResult},
	}
	for _, seed := range seeds {
		if err := w.put(tx, "studio_operations", map[string]any{
			"id": "stoper_mockdata_" + seed.suffix, "system_account_id": guard.adminID,
			"client_operation_id": "mockdata-op-" + seed.suffix,
			"kind":                seed.kind, "asset_id": seed.assetID,
			"result_json": seed.resultJSON,
			"created_at":  w.at(-46 * time.Hour), "updated_at": w.at(-46 * time.Hour),
		}); err != nil {
			return err
		}
	}
	return nil
}

// seedStudioUsageLedger 按 studio_assets 样本行求和重写 admin 的配额账本行
// （契约 §9：样本含账本行；配额账本一致 = bytes 求和 + 行数）。开发库数据一律
// 是 mock 数据（cleanup.go resetAggregationState 同款裁决），owner=admin 的
// 账本行按当前资产行整行重写是确定性的选择；真实用户的账本行不受影响。
func (w *businessWriter) seedStudioUsageLedger(tx *sql.Tx, guard businessGuard) error {
	var totalBytes, totalCount int64
	if err := tx.QueryRowContext(w.ctx,
		"SELECT COALESCE(SUM(bytes), 0), COUNT(*) FROM studio_assets WHERE system_account_id = ?",
		guard.adminID).Scan(&totalBytes, &totalCount); err != nil {
		return fmt.Errorf("汇总 studio 资产账本: %w", err)
	}
	if err := w.put(tx, "studio_user_asset_usage", map[string]any{
		"system_account_id": guard.adminID,
		"asset_bytes":       totalBytes, "asset_count": totalCount,
		"updated_at": w.stamp(),
	}); err != nil {
		return err
	}
	return nil
}
