package playground

// 演练场图像生成任务存储（设计 §3.4/§5.1，v1.2 异步生图工作台）。
//
// 任务记录行落 juhe_business.playground_image_jobs（maintenance schema 已预
// 置，主键 (system_account_id,id)）；结果图与参考图仍存 playground_artifacts
// （kind=image_result/image_reference），经 job_id 列关联任务。图像字节保留
// 7 天（§3.4.5 用户裁决），到期由 jobs 项目定时任务删字节并置
// images_expired——本 store 只做读时判定与记录行状态机；记录行删除仅
// §3.4.6 v1.5 手动删除入口（终态任务行与产物字节同事务一并删）。
//
// 状态机（§3.4.2 执行器口径）：
//
//	queued --Claim(原子)--> running --Complete--> completed(expires_at=完成+7d)
//	                                     \--Fail----> failed(error_message=上游原文摘要)
//	启动恢复：queued 重返调用方入队；running 置 failed("网关重启，任务中断")。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// playgroundImageJobsTable 是图像任务记录表（DDL 见 maintenance
// pg_schema_business_tables.go；idx_playground_image_jobs_account_created 支
// 撑列表倒序分页，idx_playground_image_jobs_expiry 支撑 jobs 到期清理）。
const playgroundImageJobsTable = "juhe_business.playground_image_jobs"

// imageJobStatus 是任务状态词表（与 schema CHECK 同源）；imageJobStatusExpired
// 与 imageJobStatusActive 是列表过滤的派生态（expired = completed 且图像已过
// 期；active = queued|running 在途），不是存储值。
const (
	imageJobStatusQueued    = "queued"
	imageJobStatusRunning   = "running"
	imageJobStatusCompleted = "completed"
	imageJobStatusFailed    = "failed"
	imageJobStatusExpired   = "expired"
	imageJobStatusActive    = "active"
)

// imageJobRetention 是 completed 任务的图像字节保留期（§3.4.5：7 天，按任务
// 完成时刻起算；任务记录行不受此限）。
const imageJobRetention = 7 * 24 * time.Hour

// 图像任务存储错误哨兵（handler 层映射：任务不存在/越权 → 404
// playground_job_not_found；非终态不可删（§3.4.6 v1.5）→ 409
// playground_job_not_deletable；参考图归属失败 → 400
// playground_reference_not_found；存储未接线 → 503；status 过滤词表外 → 400；
// 其余 → 500）。
var (
	errImageJobNotFound          = errors.New("playground image job not found")
	errImageJobNotDeletable      = errors.New("playground image job not deletable")
	errImageJobReferenceNotFound = errors.New("playground image job reference not found")
	errImageJobStoreUnavailable  = errors.New("playground image job store unavailable")
	errImageJobFilterInvalid     = errors.New("playground image job filter invalid")
)

// ImageJobRecord 是任务记录行的完整投影（写入输入 + 执行器派发输入）。
// Token/费用/终态字段由执行器经 Complete/Fail 回填；ViewerID 即
// system_account_id 隔离键。时间戳均为 UnixMilli。APIKeyID/ProviderCode 是
// 真实 Key 化修订新增的 params 投影（无 schema 迁移，落 params jsonb）：
// 执行器按 APIKeyID 派发（按该 Key 计费）、按 ProviderCode 本地计价；存量
// 行（params 缺 apiKeyId）执行时按失败收敛。
type ImageJobRecord struct {
	ViewerID      string
	ID            string
	Status        string
	APIKeyID      string
	ProviderCode  string
	ModelID       string
	Prompt        string
	AspectRatio   string
	Resolution    string
	Quality       string
	OutputFormat  string
	Size          string // 实际发送上游的 "WxH"（两者皆省略时为空 = 上游默认）
	ReferenceIDs  []string
	ResultIDs     []string
	InputTokens   int64
	OutputTokens  int64
	TotalTokens   int64
	CostUSD       *float64
	ErrorMessage  string
	ImagesExpired bool
	ExpiresAt     int64
	CreatedAt     int64
	UpdatedAt     int64
	CompletedAt   int64
}

// ImageJobUsage 是任务用量（§3.4.2：解析上游响应 usage，宽容，缺省 0；仅
// completed 卡片有值）。
type ImageJobUsage struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	TotalTokens  int64 `json:"totalTokens"`
}

// imageResultEntry 复用 images.go 的 {id,url} 条目形状（§3.4 卡片契约同形）。

// ImageJobCard 是任务卡片（§3.4.3/§3.4.4 列表与详情同形）。created 形状归一
// 见 imageJobCardOf。
type ImageJobCard struct {
	JobID  string `json:"jobId"`
	Status string `json:"status"`
	Prompt string `json:"prompt"`
	Model  string `json:"model"`
	// APIKeyID 是创建时所选 API Key（§4.5：卡片行携带，供前端回填弹窗与
	// 「Key 已删除」占位；来源 params jsonb 的 apiKeyId，存量行为空串）。
	APIKeyID       string             `json:"apiKeyId"`
	AspectRatio    string             `json:"aspectRatio"`
	Resolution     string             `json:"resolution"`
	Quality        string             `json:"quality"`
	OutputFormat   string             `json:"outputFormat"`
	Size           string             `json:"size"`
	ReferenceCount int                `json:"referenceCount"`
	References     []imageResultEntry `json:"references"`
	Images         []imageResultEntry `json:"images"`
	ImageDeleted   bool               `json:"imageDeleted"`
	Usage          *ImageJobUsage     `json:"usage"`
	CostUSD        *float64           `json:"costUsd"`
	Error          string             `json:"error"`
	CreatedAt      int64              `json:"createdAt"`
	CompletedAt    int64              `json:"completedAt"`
	ExpiresAt      int64              `json:"expiresAt"`
}

// ImageJobFilter 是 GET /images/jobs 的查询过滤（§3.4.3）。Status 接受四存储
// 值 + expired/active 派生态；Limit 默认 24、上限 100；Before 为 created_at
// UnixMilli 游标（严格小于，按创建时间倒序）。
type ImageJobFilter struct {
	Keyword  string
	APIKeyID string // 按 params jsonb 的 apiKeyId 过滤（创建时 Key）
	Status   string
	Quality  string
	Limit    int
	Before   int64
}

// ImageJobPage 是列表响应（§3.4.3：{jobs, nextCursor}；nextCursor 缺省 = 无
// 更多，omitempty 保证响应形状收敛）。
type ImageJobPage struct {
	Jobs       []ImageJobCard `json:"jobs"`
	NextCursor int64          `json:"nextCursor,omitempty"`
}

// ImageJobStore 是图像任务存储后端（导出接口，生产装配
// NewPGImageJobStore，测试注入 fake）。所有查询按 (ViewerID, id) 定位，跨用
// 户不可达（404 语义）。nil 实现 = 图像任务面未接线，端点显式降级 503。
type ImageJobStore interface {
	// Create 落 queued 行并在同一事务内把参考图 artifacts 行绑定 job_id（校
	// 验归属：本人 + kind='image_reference' + 未删；任一缺失整体回滚并返回
	// errImageJobReferenceNotFound）。返回补全 id/created_at 的记录。
	Create(job *ImageJobRecord) (ImageJobRecord, error)
	// Claim 原子置 queued→running，返回是否抢到（启动恢复与慢 goroutine 竞
	// 态防线的原子口径）。
	Claim(viewerID, jobID string) (bool, error)
	// Complete 置 completed，回填 result_artifact_ids/usage/cost_usd 并置
	// expires_at=完成时刻+7d（UnixMilli）；size 非空时回写 params.size。
	Complete(viewerID, jobID string, resultIDs []string, usage ImageJobUsage, costUSD *float64, size string) error
	// Fail 置 failed 并存 error_message（上游错误原文摘要），无图无 expires_at。
	Fail(viewerID, jobID, message string) error
	// Get 读取单张卡片（§3.4.4 详情形状）；不存在/越权返回 errImageJobNotFound。
	Get(viewerID, jobID string) (ImageJobCard, error)
	// List 按过滤条件返回卡片页（created_at DESC；多取一条探测 nextCursor）。
	List(viewerID string, filter ImageJobFilter) (ImageJobPage, error)
	// Delete 手动删除任务（§3.4.6 v1.5）：仅终态（completed 含图像已删/failed）
	// 可删，任务行与产物字节（image_result + image_reference）在同一事务内一并
	// 删除；不存在/越权返回 errImageJobNotFound，非终态（queued/running）返回
	// errImageJobNotDeletable。
	Delete(viewerID, jobID string) error
	// RecoverAtBoot 返回全部 queued 行（供重启后重新入队）并把全部 running 行
	// 置 failed（"网关重启，任务中断"；goroutine 已随进程死亡，不可续）。
	RecoverAtBoot() ([]ImageJobRecord, error)
}

// NewPGImageJobStore 构造生产任务存储；db 为 nil 时视为未接线（端点降级）。
func NewPGImageJobStore(db *sql.DB) ImageJobStore {
	return &pgImageJobStore{db: db}
}

type pgImageJobStore struct{ db *sql.DB }

func (s *pgImageJobStore) available() bool { return s != nil && s.db != nil }

// imageJobParams 是 params JSONB 的形状（§5.1：aspectRatio/resolution/quality/
// outputFormat/size(实际发送)/referenceCount；真实 Key 化修订追加
// apiKeyId/providerCode——任务派发与计价的依据，无 schema 迁移）。
type imageJobParams struct {
	AspectRatio    string `json:"aspectRatio,omitempty"`
	Resolution     string `json:"resolution,omitempty"`
	Quality        string `json:"quality,omitempty"`
	OutputFormat   string `json:"outputFormat,omitempty"`
	Size           string `json:"size,omitempty"`
	ReferenceCount int    `json:"referenceCount,omitempty"`
	APIKeyID       string `json:"apiKeyId,omitempty"`
	ProviderCode   string `json:"providerCode,omitempty"`
}

// Create implements ImageJobStore. 参考图绑定与任务落库同事务：UPDATE 命中行
// 数必须与去重后的参考图数一致（本人 + kind='image_reference' + 未删），否则
// 整体回滚返回 errImageJobReferenceNotFound。
func (s *pgImageJobStore) Create(job *ImageJobRecord) (ImageJobRecord, error) {
	if err := validateArtifactViewer(job.ViewerID); err != nil {
		return ImageJobRecord{}, err
	}
	if !s.available() {
		return ImageJobRecord{}, errImageJobStoreUnavailable
	}
	references := dedupeNonEmptyStrings(job.ReferenceIDs)
	jobID, idErr := newArtifactID()
	if idErr != nil {
		return ImageJobRecord{}, idErr
	}
	now := time.Now().UnixMilli()
	params, paramErr := json.Marshal(imageJobParams{
		AspectRatio: job.AspectRatio, Resolution: job.Resolution, Quality: job.Quality,
		OutputFormat: job.OutputFormat, Size: job.Size, ReferenceCount: len(references),
		APIKeyID: job.APIKeyID, ProviderCode: job.ProviderCode,
	})
	if paramErr != nil {
		return ImageJobRecord{}, paramErr
	}
	referenceJSON, paramErr := json.Marshal(references)
	if paramErr != nil {
		return ImageJobRecord{}, paramErr
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return ImageJobRecord{}, err
	}
	defer tx.Rollback()
	// account_id 是历史列（v1.4 口径恒空串）：Key 归属随 params.apiKeyId 落
	// jsonb，无 schema 迁移。
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO `+playgroundImageJobsTable+`
			(system_account_id,id,status,account_id,model_id,prompt,params,reference_artifact_ids,expires_at,created_at,updated_at)
			VALUES ($1,$2,'`+imageJobStatusQueued+`','',$3,$4,$5::jsonb,$6::jsonb,0,$7,$7)`,
		job.ViewerID, jobID, job.ModelID, job.Prompt, string(params), string(referenceJSON), now); err != nil {
		return ImageJobRecord{}, err
	}
	if len(references) > 0 {
		bound, err := tx.ExecContext(context.Background(),
			`UPDATE `+playgroundArtifactsTable+` SET job_id=$3
			 WHERE system_account_id=$1 AND kind='image_reference' AND id = ANY($2::text[])`,
			job.ViewerID, references, jobID)
		if err != nil {
			return ImageJobRecord{}, err
		}
		if affected, err := bound.RowsAffected(); err != nil || int(affected) != len(references) {
			return ImageJobRecord{}, errImageJobReferenceNotFound
		}
	}
	if err := tx.Commit(); err != nil {
		return ImageJobRecord{}, err
	}
	created := *job
	created.ID = jobID
	created.Status = imageJobStatusQueued
	created.CreatedAt = now
	created.UpdatedAt = now
	created.ReferenceIDs = references
	return created, nil
}

// Claim implements ImageJobStore. WHERE status='queued' 保证原子性：并发
// claim 只有一方 RowsAffected=1。
func (s *pgImageJobStore) Claim(viewerID, jobID string) (bool, error) {
	if err := validateArtifactViewer(viewerID); err != nil {
		return false, err
	}
	if !s.available() {
		return false, errImageJobStoreUnavailable
	}
	result, err := s.db.ExecContext(context.Background(),
		`UPDATE `+playgroundImageJobsTable+` SET status='`+imageJobStatusRunning+`', updated_at=$3
		 WHERE system_account_id=$1 AND id=$2 AND status='`+imageJobStatusQueued+`'`,
		viewerID, jobID, time.Now().UnixMilli())
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// Complete implements ImageJobStore. completed_at 取完成时刻，expires_at =
// 完成时刻 + 7d（§3.4.5 图像字节保留期；前端按 expires_at-now 展示「X 天 X 时
// 后删除图像」）。size 经 jsonb_set 回写 params.size（实际发送值落卡片的 size
// 标签口径）。
func (s *pgImageJobStore) Complete(viewerID, jobID string, resultIDs []string, usage ImageJobUsage, costUSD *float64, size string) error {
	if err := validateArtifactViewer(viewerID); err != nil {
		return err
	}
	if !s.available() {
		return errImageJobStoreUnavailable
	}
	resultJSON, err := json.Marshal(dedupeNonEmptyStrings(resultIDs))
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	expiresAt := now + imageJobRetention.Milliseconds()
	_, err = s.db.ExecContext(context.Background(),
		`UPDATE `+playgroundImageJobsTable+` SET
			status='`+imageJobStatusCompleted+`', result_artifact_ids=$3::jsonb,
			input_tokens=$4, output_tokens=$5, total_tokens=$6, cost_usd=$7,
			expires_at=$8, completed_at=$9, updated_at=$9,
			params = CASE WHEN $10 <> '' THEN jsonb_set(params,'{size}',to_jsonb($10::text),true) ELSE params END
		 WHERE system_account_id=$1 AND id=$2`,
		viewerID, jobID, string(resultJSON), usage.InputTokens, usage.OutputTokens, usage.TotalTokens,
		costUSD, expiresAt, now, size)
	return err
}

// Fail implements ImageJobStore.
func (s *pgImageJobStore) Fail(viewerID, jobID, message string) error {
	if err := validateArtifactViewer(viewerID); err != nil {
		return err
	}
	if !s.available() {
		return errImageJobStoreUnavailable
	}
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE `+playgroundImageJobsTable+` SET status='`+imageJobStatusFailed+`', error_message=$3, updated_at=$4
		 WHERE system_account_id=$1 AND id=$2`,
		viewerID, jobID, message, time.Now().UnixMilli())
	return err
}

// imageJobRowScan 是记录行查询的共享列清单与扫描目标。
const imageJobRowColumns = `system_account_id,id,status,model_id,prompt,params,
	result_artifact_ids,reference_artifact_ids,input_tokens,output_tokens,total_tokens,cost_usd,
	error_message,images_expired,expires_at,created_at,updated_at,completed_at`

// scanImageJobRow 把一行记录扫描进 ImageJobRecord（params/ids JSONB 解析失败
// 视为空形状——记录行是长期数据，脏参数不阻断历史展示）。
func scanImageJobRow(scan func(dest ...any) error) (ImageJobRecord, error) {
	var record ImageJobRecord
	var params, resultJSON, referenceJSON []byte
	var costUSD sql.NullFloat64
	if err := scan(&record.ViewerID, &record.ID, &record.Status, &record.ModelID,
		&record.Prompt, &params, &resultJSON, &referenceJSON,
		&record.InputTokens, &record.OutputTokens, &record.TotalTokens, &costUSD,
		&record.ErrorMessage, &record.ImagesExpired, &record.ExpiresAt,
		&record.CreatedAt, &record.UpdatedAt, &record.CompletedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ImageJobRecord{}, errImageJobNotFound
		}
		return ImageJobRecord{}, err
	}
	if costUSD.Valid {
		value := costUSD.Float64
		record.CostUSD = &value
	}
	record.AspectRatio, record.Resolution, record.Quality, record.OutputFormat, record.Size, record.APIKeyID, record.ProviderCode = decodeImageJobParams(params)
	record.ResultIDs = decodeStringArray(resultJSON)
	record.ReferenceIDs = decodeStringArray(referenceJSON)
	return record, nil
}

// decodeImageJobParams 解析 params JSONB（非法/空形状返回零值组；APIKeyID/
// ProviderCode 随参数投影——存量行两字段为空，执行时按失败收敛）。
func decodeImageJobParams(raw []byte) (aspectRatio, resolution, quality, outputFormat, size, apiKeyID, providerCode string) {
	var params imageJobParams
	if json.Unmarshal(raw, &params) != nil {
		return "", "", "", "", "", "", ""
	}
	return params.AspectRatio, params.Resolution, params.Quality, params.OutputFormat, params.Size, params.APIKeyID, params.ProviderCode
}

// decodeStringArray 解析 ids JSONB 数组（非法/空返回 nil）。
func decodeStringArray(raw []byte) []string {
	var values []string
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	return values
}

// Get implements ImageJobStore.
func (s *pgImageJobStore) Get(viewerID, jobID string) (ImageJobCard, error) {
	if err := validateArtifactViewer(viewerID); err != nil {
		return ImageJobCard{}, err
	}
	if !s.available() {
		return ImageJobCard{}, errImageJobStoreUnavailable
	}
	record, err := scanImageJobRow(func(dest ...any) error {
		return s.db.QueryRowContext(context.Background(),
			`SELECT `+imageJobRowColumns+` FROM `+playgroundImageJobsTable+` WHERE system_account_id=$1 AND id=$2`,
			viewerID, jobID).Scan(dest...)
	})
	if err != nil {
		return ImageJobCard{}, err
	}
	return imageJobCardOf(record, time.Now()), nil
}

// List implements ImageJobStore. 过滤拼装：keyword 对 prompt ILIKE，通配符
// 与转义符（%/_/\）进 LIKE 前转义（ESCAPE '\'），按展示面语义字面匹配——搜
// 索框输入「100%」找的是含字面 % 的提示词，不是任意串；expired 是 completed
// 的派生态（读时判定 images_expired 或 expires_at 已过），completed 过滤则
// 收敛为未删完成，两桶不相交。游标取 created_at 严格小于（UnixMilli 粒度下
// 同毫秒任务按用户生成节奏可忽略；order 追加 id DESC 保证同刻稳定序）。
func (s *pgImageJobStore) List(viewerID string, filter ImageJobFilter) (ImageJobPage, error) {
	if err := validateArtifactViewer(viewerID); err != nil {
		return ImageJobPage{}, err
	}
	if !s.available() {
		return ImageJobPage{}, errImageJobStoreUnavailable
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 24
	}
	if limit > 100 {
		limit = 100
	}
	query := `SELECT ` + imageJobRowColumns + ` FROM ` + playgroundImageJobsTable + ` WHERE system_account_id=$1`
	args := []any{viewerID}
	arg := func(value any) string {
		args = append(args, value)
		return "$" + strconv.Itoa(len(args))
	}
	if filter.Keyword != "" {
		query += ` AND prompt ILIKE '%'||` + arg(escapeLikePattern(filter.Keyword)) + `||'%' ESCAPE '\'`
	}
	if filter.APIKeyID != "" {
		// v1.4 口径：按创建时 Key 过滤（account_id 历史列恒空，归属在 params）。
		query += ` AND params->>'apiKeyId' = ` + arg(filter.APIKeyID)
	}
	now := time.Now().UnixMilli()
	switch filter.Status {
	case imageJobStatusExpired:
		query += ` AND status='` + imageJobStatusCompleted + `' AND (images_expired OR (expires_at > 0 AND expires_at < ` + arg(now) + `))`
	case imageJobStatusCompleted:
		query += ` AND status='` + imageJobStatusCompleted + `' AND NOT (images_expired OR (expires_at > 0 AND expires_at < ` + arg(now) + `))`
	case imageJobStatusActive:
		query += ` AND status IN ('` + imageJobStatusQueued + `','` + imageJobStatusRunning + `')`
	case imageJobStatusQueued, imageJobStatusRunning, imageJobStatusFailed:
		query += ` AND status = ` + arg(filter.Status)
	case "":
	default:
		return ImageJobPage{}, errImageJobFilterInvalid
	}
	if filter.Quality != "" {
		query += ` AND params->>'quality' = ` + arg(filter.Quality)
	}
	if filter.Before > 0 {
		query += ` AND created_at < ` + arg(filter.Before)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ` + arg(limit+1)
	rows, err := s.db.QueryContext(context.Background(), query, args...)
	if err != nil {
		return ImageJobPage{}, err
	}
	defer rows.Close()
	records := make([]ImageJobRecord, 0, limit+1)
	for rows.Next() {
		record, scanErr := scanImageJobRow(rows.Scan)
		if scanErr != nil {
			return ImageJobPage{}, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return ImageJobPage{}, err
	}
	nowTime := time.Now()
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	page := ImageJobPage{Jobs: make([]ImageJobCard, 0, len(records))}
	for _, record := range records {
		page.Jobs = append(page.Jobs, imageJobCardOf(record, nowTime))
	}
	if hasMore && len(page.Jobs) > 0 {
		page.NextCursor = records[len(records)-1].CreatedAt
	}
	return page, nil
}

// escapeLikePattern 转义 LIKE/ILIKE 的通配符与转义符（\ → \\、% → \%、
// _ → \_），配合 List 里的 ESCAPE '\' 使 keyword 字面匹配（M5；NewReplacer
// 单趟替换、替换产物不重扫，转义符替换不会二次转义）。
func escapeLikePattern(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

// Delete implements ImageJobStore. 手动删除（§3.4.6 v1.5）：事务内先按
// (system_account_id, id) 读状态——ErrNoRows → errImageJobNotFound（不存在/
// 越权统一口径，不泄露他户任务存在性）；queued/running →
// errImageJobNotDeletable（生成中不做取消语义，等终态后可删）；然后任务行与
// 产物字节（image_result + image_reference，经 job_id 列关联）在同一事务内
// 一并删除——区别于到期清理的「删字节留记录」，不可恢复。
func (s *pgImageJobStore) Delete(viewerID, jobID string) error {
	if err := validateArtifactViewer(viewerID); err != nil {
		return err
	}
	if !s.available() {
		return errImageJobStoreUnavailable
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(context.Background(),
		`SELECT status FROM `+playgroundImageJobsTable+` WHERE system_account_id=$1 AND id=$2`,
		viewerID, jobID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errImageJobNotFound
		}
		return err
	}
	if status == imageJobStatusQueued || status == imageJobStatusRunning {
		return errImageJobNotDeletable
	}
	if _, err := tx.ExecContext(context.Background(),
		`DELETE FROM `+playgroundArtifactsTable+` WHERE system_account_id=$1 AND job_id=$2 AND kind IN ('image_result','image_reference')`,
		viewerID, jobID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(context.Background(),
		`DELETE FROM `+playgroundImageJobsTable+` WHERE system_account_id=$1 AND id=$2`,
		viewerID, jobID); err != nil {
		return err
	}
	return tx.Commit()
}

// RecoverAtBoot implements ImageJobStore. 单事务内先读 queued 再把 running 全
// 部置 failed（设计 §3.4.2 启动恢复原文：queued 重新入队继续执行，running 置
// failed——执行 goroutine 已随进程死亡）。
func (s *pgImageJobStore) RecoverAtBoot() ([]ImageJobRecord, error) {
	if !s.available() {
		return nil, errImageJobStoreUnavailable
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(context.Background(),
		`SELECT `+imageJobRowColumns+` FROM `+playgroundImageJobsTable+` WHERE status='`+imageJobStatusQueued+`' ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	queued := make([]ImageJobRecord, 0, 4)
	for rows.Next() {
		record, scanErr := scanImageJobRow(rows.Scan)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		queued = append(queued, record)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(context.Background(),
		`UPDATE `+playgroundImageJobsTable+` SET status='`+imageJobStatusFailed+`', error_message=$1, updated_at=$2
		 WHERE status='`+imageJobStatusRunning+`'`,
		"网关重启，任务中断", time.Now().UnixMilli()); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return queued, nil
}

// imageJobCardOf 把记录行归一为 §3.4 卡片契约（列表与详情同形）：
//   - imageDeleted 读时判定：expires_at 已过或清理标记 images_expired 任一为
//     真（字节删除可滞后于判定，展示恒收敛，§3.4.4）；
//   - completed 且未删才给 images/references 的 {id,url}（url 复用
//     artifactContentURL 相对地址），过期/失败为空数组；
//   - completed（含已删）都带 usage/costUsd（M1：历史记录价值，数据库有值就
//     展示；costUsd=null 表示目录无价，不虚构金额）；expiresAt 维持「仅
//     completed 未删」收敛（已删后无删除倒计时，expiresAt=0）；
//   - 时间戳一律 unix 秒。
func imageJobCardOf(record ImageJobRecord, now time.Time) ImageJobCard {
	nowMilli := now.UnixMilli()
	deleted := record.ImagesExpired || (record.ExpiresAt > 0 && record.ExpiresAt < nowMilli)
	card := ImageJobCard{
		JobID:          record.ID,
		Status:         record.Status,
		Prompt:         record.Prompt,
		Model:          record.ModelID,
		APIKeyID:       record.APIKeyID,
		AspectRatio:    record.AspectRatio,
		Resolution:     record.Resolution,
		Quality:        record.Quality,
		OutputFormat:   record.OutputFormat,
		Size:           record.Size,
		ReferenceCount: len(record.ReferenceIDs),
		References:     []imageResultEntry{},
		Images:         []imageResultEntry{},
		ImageDeleted:   deleted,
		Error:          record.ErrorMessage,
		CreatedAt:      record.CreatedAt / 1000,
		CompletedAt:    record.CompletedAt / 1000,
	}
	if record.Status != imageJobStatusCompleted {
		return card
	}
	if !deleted {
		for _, id := range record.ResultIDs {
			if id != "" {
				card.Images = append(card.Images, imageResultEntry{ID: id, URL: artifactContentURL(id)})
			}
		}
		for _, id := range record.ReferenceIDs {
			if id != "" {
				card.References = append(card.References, imageResultEntry{ID: id, URL: artifactContentURL(id)})
			}
		}
		card.ExpiresAt = record.ExpiresAt / 1000
	}
	card.Usage = &ImageJobUsage{
		InputTokens:  record.InputTokens,
		OutputTokens: record.OutputTokens,
		TotalTokens:  record.TotalTokens,
	}
	card.CostUSD = record.CostUSD
	return card
}

// dedupeNonEmptyStrings 保序去重并剔除空串（referenceIds 绑定校验按去重集合
// 精确比对 RowsAffected，重复项会造成误判）。
func dedupeNonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
