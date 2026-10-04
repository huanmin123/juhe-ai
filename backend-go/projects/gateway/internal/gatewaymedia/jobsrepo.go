// jobsrepo.go 是 media_jobs 表的网关侧仓储（媒体设计 §8.2：id 对外、
// account_id/provider_code/provider_protocol_profile_id 账户亲和三元组、
// 状态机/终态回填字段）。SQL 风格沿 chainAccountsSelector 先例：SQLite ?
// 占位 + PG $n 改写、postgres 时 schema 限定 juhe_business。本文件只承载
// 行存取与 JSON 列编解码，不触 chain/HTTP 执行（链上层 chain_media_video.go
// 消费）。
//
// progress 不持久化：media_jobs DDL（maintenance pg_schema_business_tables.go
// / sqlite_schema_business.go，W1 已建）无 progress 列；轮询响应的 progress
// 直接取上游归一 IR 的即时值回显，列表与终态行省略该字段。
package gatewaymedia

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// MediaJobRetentionTTL 是任务行保留时长（媒体设计 §8.2 清理任务：TTL 过期置
// expired / 删行；openai content 上游时效约 1 小时、GCS 签名 URL 约 2 天，
// 7 天覆盖全部在册厂商的产物时效余量）。
const MediaJobRetentionTTL = 7 * 24 * time.Hour

// MediaJobRecord 是 media_jobs 一行的编解码载体。JSON 列以结构化字段承载：
// RequestSnapshot 仅轻量参数元数据（媒体设计 §8.1.4 零资源存储：不含 base64
// 资源字段）+ 创建时冻结的记账 scope 五元组（终态 usage 行身份维度，§10），
// Artifact 只存上游定位指针（ContentURL），Error/Usage 为终态回填面；
// CostUsd 是终态计费回填列（M2：轮询终态时按 OutputVideoSeconds × 秒价
// 算出随 UpdateTerminal 落行；非终态行保持 0）。
type MediaJobRecord struct {
	ID                        string
	Kind                      MediaJobKind
	APIKeyID                  string
	AccountID                 string
	ProviderCode              string
	ProviderProtocolProfileID string
	UpstreamJobID             string
	Status                    MediaJobStatus
	RequestSnapshot           MediaJobRequestSnapshot
	Artifact                  MediaJobArtifact
	Error                     *MediaJobError
	Usage                     MediaJobUsage
	CostUsd                   float64
	ParamsApplied             []string
	ParamsIgnored             []string
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

// MediaJobRequestSnapshot 是 request_snapshot_json 的轻量参数元数据（零资源
// 存储：model/prompt/seconds/size/n 等，不存 input_reference 等资源字段）。
// Account/Group scope 字段是创建时从派发上下文（UsageContext + 账户视图
// BoundGroupID 覆盖语义）一次性冻结的记账五元组：任务终态在轮询请求上驱动
// （usage 行身份维度），轮询时派发上下文已不可得，故随行携带（纯元数据，
// 不违零资源存储原则）。
type MediaJobRequestSnapshot struct {
	Model   string   `json:"model,omitempty"`
	Prompt  string   `json:"prompt,omitempty"`
	Seconds *float64 `json:"seconds,omitempty"`
	Size    string   `json:"size,omitempty"`
	N       int      `json:"n,omitempty"`
	// ProviderOptionsApplied 是 provider_options 命中子对象的键名摘要
	//（不含值，契约 §2.4 规则 4 回显）：创建时随快照冻结，任务面/列表的
	// job 对象回显同一份数据（终态后原始请求不可得）。
	ProviderOptionsApplied []string `json:"providerOptionsApplied,omitempty"`
	// Account scope 五元组（account_access_type=account_authorized 时
	// accountAuthorizationID 必填，否则归一化清空整组——NormalizeUsageRecordInput 规则）。
	AccountAccessType                string `json:"accountAccessType,omitempty"`
	AccountAuthorizationID           string `json:"accountAuthorizationId,omitempty"`
	AccountAuthorizationSourceType   string `json:"accountAuthorizationSourceType,omitempty"`
	AccountAuthorizationSourceTeamID string `json:"accountAuthorizationSourceTeamId,omitempty"`
	// Group scope 五元组（groupAccess=authorized 时 groupAuthorizationID 必填）。
	GroupID                          string `json:"groupId,omitempty"`
	GroupOwnerSystemAccountID        string `json:"groupOwnerSystemAccountId,omitempty"`
	GroupAccessType                  string `json:"groupAccessType,omitempty"`
	GroupAuthorizationID             string `json:"groupAuthorizationId,omitempty"`
	GroupAuthorizationSourceType     string `json:"groupAuthorizationSourceType,omitempty"`
	GroupAuthorizationSourceTeamID   string `json:"groupAuthorizationSourceTeamId,omitempty"`
}

// ErrMediaJobNotFound 是 GetByIDAndAPIKey 未命中（不存在或非本人任务）的哨兵
// 错误；链上层按 404 语义渲染，不区分两种情况（归属信息不泄露）。
var ErrMediaJobNotFound = errors.New("媒体任务不存在")

// MediaJobsRepo 是 media_jobs 的行仓储。
type MediaJobsRepo struct {
	db       *sql.DB
	postgres bool
	now      func() time.Time
}

// NewMediaJobsRepo 构造仓储；postgres 为真时查询按 $n 方言绑定并限定
// juhe_business schema。now 注入时间源（测试可固定）。
func NewMediaJobsRepo(db *sql.DB, postgres bool, now func() time.Time) *MediaJobsRepo {
	if now == nil {
		now = time.Now
	}
	return &MediaJobsRepo{db: db, postgres: postgres, now: now}
}

func (r *MediaJobsRepo) table() string {
	if r.postgres {
		return "juhe_business.media_jobs"
	}
	return "media_jobs"
}

// bind 把 ? 占位改写为 PG $n（与 chainAccountsSelector.bind 同构）。
func (r *MediaJobsRepo) bind(query string) string {
	if !r.postgres {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + fmt.Sprint(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

// Insert 落一行新任务（受理凭据确立后：2xx + 上游 job id）。ID 由调用方生成
// （对外 id，不用上游 id，媒体设计 §8.2）。
func (r *MediaJobsRepo) Insert(ctx context.Context, record MediaJobRecord) error {
	now := r.now().UTC()
	snapshotJSON, err := json.Marshal(record.RequestSnapshot)
	if err != nil {
		return fmt.Errorf("编码媒体任务请求快照失败: %w", err)
	}
	artifactJSON, err := json.Marshal(record.Artifact)
	if err != nil {
		return fmt.Errorf("编码媒体任务产物定位失败: %w", err)
	}
	errorJSON, err := marshalMediaJobError(record.Error)
	if err != nil {
		return err
	}
	usageJSON, err := marshalMediaJobUsage(record.Usage)
	if err != nil {
		return err
	}
	applied := marshalStringList(record.ParamsApplied)
	ignored := marshalStringList(record.ParamsIgnored)
	_, err = r.db.ExecContext(ctx, r.bind(`INSERT INTO `+r.table()+` (
		id, kind, api_key_id, account_id, provider_code, provider_protocol_profile_id,
		upstream_job_id, status, request_snapshot_json, artifact_json, error_json, usage_json,
		params_applied_json, params_ignored_json, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		record.ID, string(record.Kind), record.APIKeyID, record.AccountID, record.ProviderCode,
		nullString(record.ProviderProtocolProfileID), record.UpstreamJobID, string(record.Status),
		string(snapshotJSON), string(artifactJSON), errorJSON, usageJSON,
		applied, ignored, mediaJobTimeLayout(now), mediaJobTimeLayout(now))
	if err != nil {
		return fmt.Errorf("写入媒体任务行失败: %w", err)
	}
	return nil
}

// UpdateStatus 推进非终态行的轮询状态（含取消置 cancelled）。WHERE 限定
// 当前行仍处非终态（queued/in_progress）：终态行不再被任何非终态写入
// 覆盖（防终态回退——并发轮询/清理与取消竞争时，先落的终态恒胜出）。
// 返回受影响行数：0 表示行已终态（或不存在），本写入未生效；调用方据此
// 决定后续动作（取消面 0 行仍是上游已确认的 204，轮询面 0 行跳过副作用）。
func (r *MediaJobsRepo) UpdateStatus(ctx context.Context, id string, status MediaJobStatus) (int64, error) {
	result, err := r.db.ExecContext(ctx, r.bind(`UPDATE `+r.table()+` SET status = ?, updated_at = ? WHERE id = ? AND status IN ('queued','in_progress')`),
		string(status), mediaJobTimeLayout(r.now().UTC()), id)
	if err != nil {
		return 0, fmt.Errorf("更新媒体任务状态失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取媒体任务状态更新行数失败: %w", err)
	}
	return affected, nil
}

// UpdateTerminal 终态回填（completed/failed/cancelled/expired）：终态状态、
// 错误摘要、产物定位、usage 终值与终态成本一次落行。costUsd 由链上层按
// usage 终值（OutputVideoSeconds × 秒价）经 pricing 引擎算出传入：completed
// 无秒（usage_missing）或目录未命中传 0（不虚计）；failed/cancelled 恒 0。
// WHERE 限定当前行仍处非终态（NOT IN 终态集）：终态只落一次——并发轮询
// 同 job 双双到达终态（或清理 job 置 expired 与网关终态竞争）时恰好一个
// 写入生效。返回受影响行数：1 = 本次终态确立（调用方仅在此分支入队 usage
// 与计费）；0 = 行已被并发方终态化，本次是重复终态，不得再入队（否则
// 同 job 产生两条 usage 行）。
func (r *MediaJobsRepo) UpdateTerminal(ctx context.Context, id string, status MediaJobStatus, jobError *MediaJobError, artifact MediaJobArtifact, usage MediaJobUsage, costUsd float64) (int64, error) {
	errorJSON, err := marshalMediaJobError(jobError)
	if err != nil {
		return 0, err
	}
	artifactJSON, err := json.Marshal(artifact)
	if err != nil {
		return 0, fmt.Errorf("编码媒体任务终态产物定位失败: %w", err)
	}
	usageJSON, err := marshalMediaJobUsage(usage)
	if err != nil {
		return 0, err
	}
	if costUsd < 0 || math.IsNaN(costUsd) || math.IsInf(costUsd, 0) {
		return 0, fmt.Errorf("媒体任务终态成本非法: %v", costUsd)
	}
	result, err := r.db.ExecContext(ctx, r.bind(`UPDATE `+r.table()+` SET status = ?, error_json = ?, artifact_json = ?, usage_json = ?, cost_usd = ?, updated_at = ? WHERE id = ? AND status NOT IN ('completed','failed','cancelled','expired')`),
		string(status), errorJSON, string(artifactJSON), usageJSON, costUsd, mediaJobTimeLayout(r.now().UTC()), id)
	if err != nil {
		return 0, fmt.Errorf("回填媒体任务终态失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取媒体任务终态回填行数失败: %w", err)
	}
	return affected, nil
}

// GetByIDAndAPIKey 按 id + 归属 API Key 取行（列表/鉴权过滤，媒体设计 §8.2）。
// 未命中（不存在或非本人）返回 ErrMediaJobNotFound。
func (r *MediaJobsRepo) GetByIDAndAPIKey(ctx context.Context, id, apiKeyID string) (*MediaJobRecord, error) {
	rows, err := r.query(ctx, `WHERE id = ? AND api_key_id = ?`, id, apiKeyID, 1, 0)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrMediaJobNotFound
	}
	return rows[0], nil
}

// ListByAPIKey 按归属 API Key 倒序分页（GET /v1/videos 列表消费）。
func (r *MediaJobsRepo) ListByAPIKey(ctx context.Context, apiKeyID string, limit, offset int) ([]*MediaJobRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	return r.query(ctx, `WHERE api_key_id = ?`, apiKeyID, limit, offset)
}

// ListExpired 取超过保留 TTL 的行（清理 job 消费：置 expired / 删行；清理
// 任务属下一任务，本仓储只提供查询）。created_at 早于 before 的行命中。
func (r *MediaJobsRepo) ListExpired(ctx context.Context, before time.Time, limit int) ([]*MediaJobRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	return r.query(ctx, `WHERE created_at < ?`, mediaJobTimeLayout(before.UTC()), limit, 0)
}

// MediaJobListQuery 是管理面任务列表的查询参数（GET /__aisys__/api/media-jobs
// 消费；空串 = 不过滤）。
type MediaJobListQuery struct {
	Status    string
	Kind      string
	APIKeyID  string
	AccountID string
	Limit     int
	Offset    int
}

// ListManagement 管理面分页查询（状态/种类/归属 Key/归属账户过滤 + 倒序
// created_at），返回行集与同条件总数（分页 envelope 的 total）。
func (r *MediaJobsRepo) ListManagement(ctx context.Context, query MediaJobListQuery) ([]*MediaJobRecord, int, error) {
	if query.Limit <= 0 || query.Limit > 100 {
		query.Limit = 20
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	where, args := mediaJobManagementWhere(query)
	total := 0
	if err := r.db.QueryRowContext(ctx, r.bind(`SELECT COUNT(*) FROM `+r.table()+` `+where), args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计媒体任务行失败: %w", err)
	}
	rows, err := r.query(ctx, where, append(args, query.Limit, query.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// mediaJobManagementWhere 构造管理面过滤 WHERE 片段（参数顺序与占位一致）。
func mediaJobManagementWhere(query MediaJobListQuery) (string, []any) {
	conditions := []string{}
	args := []any{}
	if status := strings.TrimSpace(query.Status); status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, status)
	}
	if kind := strings.TrimSpace(query.Kind); kind != "" {
		conditions = append(conditions, "kind = ?")
		args = append(args, kind)
	}
	if apiKeyID := strings.TrimSpace(query.APIKeyID); apiKeyID != "" {
		conditions = append(conditions, "api_key_id = ?")
		args = append(args, apiKeyID)
	}
	if accountID := strings.TrimSpace(query.AccountID); accountID != "" {
		conditions = append(conditions, "account_id = ?")
		args = append(args, accountID)
	}
	if len(conditions) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(conditions, " AND "), args
}

// query 是共用的行读取（WHERE 片段 + 分页；倒序 created_at DESC, id DESC 稳定
// 排序）。args 尾部两个整数是 LIMIT / OFFSET（管理面查询与归属过滤共用）。
// JSON 列解码失败按坏行跳过（行级容错：单行损坏不拖垮列表/轮询）。
func (r *MediaJobsRepo) query(ctx context.Context, where string, args ...any) ([]*MediaJobRecord, error) {
	stmt := `SELECT id, kind, api_key_id, account_id, provider_code, provider_protocol_profile_id,
		upstream_job_id, status, request_snapshot_json, artifact_json, error_json, usage_json, cost_usd,
		params_applied_json, params_ignored_json, created_at, updated_at
		FROM ` + r.table() + ` ` + where + ` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`
	return r.scanRows(ctx, r.bind(stmt), args...)
}

// scanRows 执行语句并解码行集（query/selectRows 共用）。
func (r *MediaJobsRepo) scanRows(ctx context.Context, stmt string, args ...any) ([]*MediaJobRecord, error) {
	dbRows, err := r.db.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("查询媒体任务行失败: %w", err)
	}
	defer dbRows.Close()
	var out []*MediaJobRecord
	for dbRows.Next() {
		var (
			record                                           MediaJobRecord
			kind, status                                     string
			providerProfileID                                sql.NullString
			snapshotJSON, artifactJSON, errorJSON, usageJSON string
			appliedJSON, ignoredJSON, createdAt, updatedAt   string
		)
		if err := dbRows.Scan(&record.ID, &kind, &record.APIKeyID, &record.AccountID, &record.ProviderCode,
			&providerProfileID, &record.UpstreamJobID, &status, &snapshotJSON, &artifactJSON, &errorJSON, &usageJSON,
			&record.CostUsd,
			&appliedJSON, &ignoredJSON, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("读取媒体任务行失败: %w", err)
		}
		record.Kind = MediaJobKind(kind)
		record.Status = MediaJobStatus(status)
		record.ProviderProtocolProfileID = providerProfileID.String
		if err := json.Unmarshal([]byte(snapshotJSON), &record.RequestSnapshot); err != nil {
			continue
		}
		if err := json.Unmarshal([]byte(artifactJSON), &record.Artifact); err != nil {
			continue
		}
		if strings.TrimSpace(errorJSON) != "" && errorJSON != "{}" {
			var jobError MediaJobError
			if err := json.Unmarshal([]byte(errorJSON), &jobError); err == nil && (jobError.Code != "" || jobError.Message != "") {
				record.Error = &jobError
			}
		}
		if err := unmarshalMediaJobUsage(usageJSON, &record.Usage); err != nil {
			continue
		}
		record.ParamsApplied = unmarshalStringList(appliedJSON)
		record.ParamsIgnored = unmarshalStringList(ignoredJSON)
		record.CreatedAt = parseMediaJobTime(createdAt)
		record.UpdatedAt = parseMediaJobTime(updatedAt)
		out = append(out, &record)
	}
	if err := dbRows.Err(); err != nil {
		return nil, fmt.Errorf("遍历媒体任务行失败: %w", err)
	}
	return out, nil
}

// marshalMediaJobError 编码终态错误摘要（空错误 → "{}"）。
func marshalMediaJobError(jobError *MediaJobError) (string, error) {
	if jobError == nil {
		return "{}", nil
	}
	encoded, err := json.Marshal(jobError)
	if err != nil {
		return "", fmt.Errorf("编码媒体任务错误摘要失败: %w", err)
	}
	return string(encoded), nil
}

// marshalMediaJobUsage 编码 usage_json：结构化字段（output_video_seconds）+
// Raw 原样保留（厂商原生用量字段，openai 无）。
func marshalMediaJobUsage(usage MediaJobUsage) (string, error) {
	payload := map[string]any{}
	if usage.OutputVideoSeconds != nil {
		payload["output_video_seconds"] = *usage.OutputVideoSeconds
	}
	for key, value := range usage.Raw {
		payload[key] = value
	}
	if len(payload) == 0 {
		return "{}", nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("编码媒体任务用量失败: %w", err)
	}
	return string(encoded), nil
}

func unmarshalMediaJobUsage(raw string, usage *MediaJobUsage) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "{}" {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return err
	}
	if seconds, ok := payload["output_video_seconds"].(float64); ok {
		value := seconds
		usage.OutputVideoSeconds = &value
		delete(payload, "output_video_seconds")
	}
	if len(payload) > 0 {
		usage.Raw = payload
	}
	return nil
}

func marshalStringList(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	encoded, _ := json.Marshal(values)
	return string(encoded)
}

func unmarshalStringList(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "[]" || trimmed == "null" {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(trimmed), &values); err != nil {
		return nil
	}
	return values
}

func nullString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// mediaJobTimeLayout / parseMediaJobTime：毫秒 RFC3339 UTC 文本（与链侧
// nowIso 同构），SQLite 与 PG 文本列共用。
func mediaJobTimeLayout(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func parseMediaJobTime(value string) time.Time {
	if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value)); err == nil {
		return parsed
	}
	return time.Time{}
}
