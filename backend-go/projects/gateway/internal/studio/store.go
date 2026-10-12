package studio

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// studio_assets / studio_user_asset_usage / studio_tasks 持久化（媒体工作室
// 设计 §3.1/§3.4/§3.2）。配额账本沿 chat_user_asset_usage 先例：入库/删除
// 在事务内以 pg_advisory_xact_lock(hashtextextended('juhe-ai:studio-asset-usage:'
// || system_account_id, 0)) 串行化增减（§3.4），SQLite 无该锁、以单连接 +
// 事务串行等价。
//
// 上传计量点（§3.6）：资产行提交事务内增量——本实现的写入面为单事务落行
// （对象先写、行与账本同事务提交），不存在持久化的 uploading 中间行，孤儿
// 上传只残留无行引用的对象、永不占额；删除事务内回落。账本与行数以事务
// 一致性维护，不做后台对账补偿。

// StudioAssetUserMaxBytes 是每用户 studio 资产总容量上限（P0 默认 20 GiB，
// §3.6：编译期常量可调、不设开关；调整随交付同步契约与容量预算）。
const StudioAssetUserMaxBytes = int64(20) * 1024 * 1024 * 1024

// StudioAssetUserMaxCount 是每用户 studio 资产总个数上限（P0 默认 2000）。
const StudioAssetUserMaxCount = int64(2000)

// Asset 是 studio_assets 行（列名严格对齐契约 §3.1）。
type Asset struct {
	ID              string
	SystemAccountID string
	Kind            string
	Mime            string
	StorageKey      string
	Bytes           int64
	Width           *int64
	Height          *int64
	DurationMs      *int64
	Sha256          string
	SourceTaskID    *string
	ParamsJSON      string
	CreatedAt       string
	UpdatedAt       string
}

// AssetColumns 是 studio_assets 的全列投影（顺序与 scanAsset 一致）。
const AssetColumns = `id, system_account_id, kind, mime, storage_key, bytes,
	width, height, duration_ms, sha256, source_task_id, params_json,
	created_at, updated_at`

func scanAsset(scan func(dest ...any) error) (*Asset, error) {
	asset := &Asset{}
	err := scan(&asset.ID, &asset.SystemAccountID, &asset.Kind, &asset.Mime,
		&asset.StorageKey, &asset.Bytes, &asset.Width, &asset.Height,
		&asset.DurationMs, &asset.Sha256, &asset.SourceTaskID, &asset.ParamsJSON,
		&asset.CreatedAt, &asset.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return asset, nil
}

// Dialect 承载数据库方言差异：PG 走 juhe_business 模式限定与 $n 占位符并
// 启用配额 advisory 锁；SQLite 裸表名 + ? 占位。由组合根按部署形态构造。
type Dialect struct {
	Postgres bool
	Schema   string
}

// Table 限定表名（PG 带业务 schema 前缀）。
func (d Dialect) Table(name string) string {
	if d.Postgres && d.Schema != "" {
		return d.Schema + "." + name
	}
	return name
}

// Bind 把 ? 占位符改写为 PG 的 $n 序列。
func (d Dialect) Bind(query string) string {
	if !d.Postgres {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + itoa(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// lockAssetUsage 在事务内取该用户配额账本的 advisory 事务锁（PG 专属）。
func (d Dialect) lockAssetUsage(tx queryer, ownerID string) error {
	if !d.Postgres {
		return nil
	}
	_, err := tx.Exec(d.Bind(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`),
		"juhe-ai:studio-asset-usage:"+ownerID)
	return err
}

// queryer 与 chatassets 同形：*sql.DB 与 *sql.Tx 结构性满足。
type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
}

// Store 是 studio 产品域的资产仓库（组合根以业务库句柄构造）。
type Store struct {
	db      *sql.DB
	dialect Dialect
}

// NewStore 构造资产仓库。
func NewStore(db *sql.DB, dialect Dialect) *Store {
	return &Store{db: db, dialect: dialect}
}

// NewAssetID 生成 stasset_ 前缀、32 位十六进制随机的资产 id（chat_asset_
// 同构形状）。
func NewAssetID() string {
	return "stasset_" + randomHex32()
}

// QueryAssets 按 SQL 投影资产行。
func (s *Store) QueryAssets(q queryer, query string, args ...any) ([]*Asset, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Asset{}
	for rows.Next() {
		asset, err := scanAsset(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, asset)
	}
	return out, rows.Err()
}

// FindAssetBySha256 在同用户范围内按内容摘要定位既有资产（§5.4 digest 同
// 用户查重的上传侧幂等：跨用户同 hash 不构成任何匹配或可见性依据）。
func (s *Store) FindAssetBySha256(ownerID, sha256 string) (*Asset, error) {
	assets, err := s.QueryAssets(s.db, s.dialect.Bind(`SELECT `+AssetColumns+` FROM `+s.dialect.Table("studio_assets")+`
		WHERE system_account_id = ? AND sha256 = ?
		ORDER BY created_at DESC, id DESC
		LIMIT 1`), ownerID, sha256)
	if err != nil {
		return nil, err
	}
	if len(assets) == 0 {
		return nil, nil
	}
	return assets[0], nil
}

// GetAsset 按 id + 归属读取资产；不存在或越权同为 (nil, nil)（路由层统一
// 404 同型，§7）。
func (s *Store) GetAsset(assetID, ownerID string) (*Asset, error) {
	assets, err := s.QueryAssets(s.db, s.dialect.Bind(`SELECT `+AssetColumns+` FROM `+s.dialect.Table("studio_assets")+`
		WHERE id = ? AND system_account_id = ?
		LIMIT 1`), assetID, ownerID)
	if err != nil {
		return nil, err
	}
	if len(assets) == 0 {
		return nil, nil
	}
	return assets[0], nil
}

// AssertQuotaAvailable 是对象写入前的快速预检（§3.6 超限语义：上传与结算
// 入库前预检）；权威判定在 CreateAsset 事务内复核，预检失败同样返回两个
// 413 领域错误，避免超限流量白白写入对象。
func (s *Store) AssertQuotaAvailable(ownerID string, incomingBytes int64) error {
	var assetBytes, assetCount int64
	err := s.db.QueryRow(s.dialect.Bind(`SELECT asset_bytes, asset_count FROM `+s.dialect.Table("studio_user_asset_usage")+`
		WHERE system_account_id = ?`), ownerID).Scan(&assetBytes, &assetCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if assetBytes+incomingBytes > StudioAssetUserMaxBytes {
		return &QuotaExceededError{}
	}
	if assetCount+1 > StudioAssetUserMaxCount {
		return &CountExceededError{}
	}
	return nil
}

// CreateAssetInput 携带单事务落行所需的全部事实。
type CreateAssetInput struct {
	ID              string
	SystemAccountID string
	Kind            string
	Mime            string
	StorageKey      string
	Bytes           int64
	Width           *int64
	Height          *int64
	DurationMs      *int64
	Sha256          string
	SourceTaskID    *string
	ParamsJSON      string
	Now             string
}

// CreateAsset 在单事务内完成配额预检 + 资产行插入 + 账本增量（§3.6 计量
// 点；§4.2 上传事务与 §5.4 结算入库共用）。paramsJson 缺省 '{}'。
func (s *Store) CreateAsset(input CreateAssetInput) (*Asset, error) {
	id := input.ID
	if id == "" {
		id = NewAssetID()
	}
	paramsJSON := strings.TrimSpace(input.ParamsJSON)
	if paramsJSON == "" {
		paramsJSON = "{}"
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := s.dialect.lockAssetUsage(tx, input.SystemAccountID); err != nil {
		return nil, err
	}
	var assetBytes, assetCount int64
	err = tx.QueryRow(s.dialect.Bind(`SELECT asset_bytes, asset_count FROM `+s.dialect.Table("studio_user_asset_usage")+`
		WHERE system_account_id = ?`), input.SystemAccountID).Scan(&assetBytes, &assetCount)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if assetBytes+input.Bytes > StudioAssetUserMaxBytes {
		return nil, &QuotaExceededError{}
	}
	if assetCount+1 > StudioAssetUserMaxCount {
		return nil, &CountExceededError{}
	}
	_, err = tx.Exec(s.dialect.Bind(`INSERT INTO `+s.dialect.Table("studio_assets")+` (
		id, system_account_id, kind, mime, storage_key, bytes, width, height, duration_ms,
		sha256, source_task_id, params_json, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		id, input.SystemAccountID, input.Kind, input.Mime, input.StorageKey, input.Bytes,
		input.Width, input.Height, input.DurationMs, input.Sha256, input.SourceTaskID,
		paramsJSON, input.Now, input.Now)
	if err != nil {
		return nil, err
	}
	if err := s.incrementAssetUsage(tx, input.SystemAccountID, input.Bytes, input.Now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetAsset(id, input.SystemAccountID)
}

// ListAssets 按归属过滤素材库列表（默认全部 kind；排序 (created_at DESC,
// id DESC)，§4.2），返回页行与过滤后总数。
func (s *Store) ListAssets(ownerID, kind string, limit, offset int) ([]*Asset, int, error) {
	kindClause := ""
	args := []any{ownerID}
	if kind != "" {
		kindClause = " AND kind = ?"
		args = append(args, kind)
	}
	var total int
	if err := s.db.QueryRow(s.dialect.Bind(`SELECT COUNT(*) FROM `+s.dialect.Table("studio_assets")+`
		WHERE system_account_id = ?`+kindClause), args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	assets, err := s.QueryAssets(s.db, s.dialect.Bind(`SELECT `+AssetColumns+` FROM `+s.dialect.Table("studio_assets")+`
		WHERE system_account_id = ?`+kindClause+`
		ORDER BY created_at DESC, id DESC
		LIMIT ? OFFSET ?`), args...)
	if err != nil {
		return nil, 0, err
	}
	return assets, total, nil
}

// DeleteAsset 在单事务内完成引用保护检查 + 删行 + 账本回落（§4.2），返回
// 被删资产（对象删除在提交后按 storage_key 执行）。引用保护（409）：存在
// settlement='pending_settled' 且 asset_id 指向该资产的 studio_tasks 行（防御
// 结算重试窗口，§4.2/§11 扩展位）。不存在或越权返回 (nil, nil)。
func (s *Store) DeleteAsset(assetID, ownerID, now string) (*Asset, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := s.dialect.lockAssetUsage(tx, ownerID); err != nil {
		return nil, err
	}
	assets, err := s.QueryAssets(tx, s.dialect.Bind(`SELECT `+AssetColumns+` FROM `+s.dialect.Table("studio_assets")+`
		WHERE id = ? AND system_account_id = ?
		LIMIT 1`), assetID, ownerID)
	if err != nil {
		return nil, err
	}
	if len(assets) == 0 {
		return nil, nil
	}
	asset := assets[0]
	var references int
	if err := tx.QueryRow(s.dialect.Bind(`SELECT COUNT(*) FROM `+s.dialect.Table("studio_tasks")+`
		WHERE asset_id = ? AND system_account_id = ? AND settlement = 'pending_settled'`),
		assetID, ownerID).Scan(&references); err != nil {
		return nil, err
	}
	if references > 0 {
		return nil, &ReferencedError{}
	}
	result, err := tx.Exec(s.dialect.Bind(`DELETE FROM `+s.dialect.Table("studio_assets")+`
		WHERE id = ? AND system_account_id = ?`), assetID, ownerID)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, nil
	}
	if err := s.decrementAssetUsage(tx, ownerID, asset.Bytes, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return asset, nil
}

// ReferencedError 是引用保护命中的领域错误（路由层映射 409
// studio_asset_referenced）。
type ReferencedError struct{}

func (e *ReferencedError) Error() string {
	return "素材正被生成任务结算引用，无法删除"
}

func (s *Store) incrementAssetUsage(tx queryer, ownerID string, quotaBytes int64, now string) error {
	// DO UPDATE 右值必须表名限定：PG 对未限定的自引用列报 42702 ambiguous
	//（chatassets 同款登记）。
	usageTable := s.dialect.Table("studio_user_asset_usage")
	_, err := tx.Exec(s.dialect.Bind(`INSERT INTO `+usageTable+` (system_account_id, asset_bytes, asset_count, updated_at)
		VALUES (?, ?, 1, ?)
		ON CONFLICT (system_account_id) DO UPDATE SET
			asset_bytes = `+usageTable+`.asset_bytes + ?, asset_count = `+usageTable+`.asset_count + 1, updated_at = ?`),
		ownerID, quotaBytes, now, quotaBytes, now)
	return err
}

func (s *Store) decrementAssetUsage(tx queryer, ownerID string, quotaBytes int64, now string) error {
	_, err := tx.Exec(s.dialect.Bind(`UPDATE `+s.dialect.Table("studio_user_asset_usage")+`
		SET asset_bytes = CASE WHEN asset_bytes >= ? THEN asset_bytes - ? ELSE 0 END,
			asset_count = CASE WHEN asset_count > 0 THEN asset_count - 1 ELSE 0 END,
			updated_at = ?
		WHERE system_account_id = ?`), quotaBytes, quotaBytes, now, ownerID)
	return err
}

// AssetParams 解析 params_json 为对象（渲染 AssetDTO.params）；空/坏值按
// 空对象处理（列 DEFAULT '{}'，坏值不阻断列表渲染）。
func AssetParams(asset *Asset) map[string]any {
	params := map[string]any{}
	if strings.TrimSpace(asset.ParamsJSON) != "" {
		_ = json.Unmarshal([]byte(asset.ParamsJSON), &params)
	}
	return params
}
