package modelcheckowner

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// VersionedModelLimits reads the immutable model-capacity snapshot used by
// full-profile long-context probes. It is a read-only Business port; schema
// creation and updates remain outside the J3b runtime.
type VersionedModelLimits struct {
	db       *sql.DB
	postgres bool
}

func NewVersionedModelLimits(db *sql.DB, postgres bool) (*VersionedModelLimits, error) {
	if db == nil {
		return nil, errors.New("J3b model limits database is required")
	}
	return &VersionedModelLimits{db: db, postgres: postgres}, nil
}

func (m *VersionedModelLimits) Version() string {
	return "business-provider-model-catalog-v1"
}

func (m *VersionedModelLimits) MaxInputTokens(providerCode, model string, _ modelcheckprofile.Protocol) (int, error) {
	if m == nil || m.db == nil || strings.TrimSpace(providerCode) == "" || strings.TrimSpace(model) == "" {
		return 0, errors.New("J3b model limit lookup is incomplete")
	}
	// v16 契约（设计 §8 去供应商化）：旧 provider_model_catalog 单表由 models
	// 承载——upstream_model 对应旧 model 列，vendor_code 对应旧 provider_code，
	// scope/status/catalog_visible 语义不变。models 的身份唯一键含
	// (vendor_code, scope, system_account_id, upstream_model)，同厂商同名
	// 上游模型可能同时存在 builtin 行与 owner 自定义行：内置行优先、再按 id
	// 升序，保证 LIMIT 1 的确定性（旧表按 provider+model 唯一，无此分支）。
	table := "models"
	placeholder := "?"
	visible := "catalog_visible=1"
	if m.postgres {
		table = "juhe_business." + table
		placeholder = "$1"
		visible = "catalog_visible=TRUE"
	}
	query := "SELECT COALESCE(NULLIF(max_input_tokens,0), context_window_tokens) FROM " + table + " WHERE vendor_code=" + placeholder + " AND upstream_model=" + func() string {
		if m.postgres {
			return "$2"
		}
		return "?"
	}() + " AND status='active' AND " + visible + " ORDER BY CASE WHEN scope='builtin' THEN 0 ELSE 1 END, id LIMIT 1"
	var limit sql.NullInt64
	if err := m.db.QueryRow(query, providerCode, model).Scan(&limit); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("J3b model limit snapshot not found for %s/%s", providerCode, model)
		}
		return 0, fmt.Errorf("read J3b model limit snapshot: %w", err)
	}
	if !limit.Valid || limit.Int64 <= 0 || limit.Int64 > 10_000_000 {
		return 0, errors.New("J3b model limit snapshot is invalid")
	}
	return int(limit.Int64), nil
}
