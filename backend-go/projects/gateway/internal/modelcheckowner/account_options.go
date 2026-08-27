package modelcheckowner

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// ModelCheckOptions returns the immutable catalog advertised by the Node
// model-check UI. It is derived from the Gateway profile catalog, not from a
// mutable database row.
func (s *BusinessTargetSource) ModelCheckOptions() ModelCheckOptions {
	models := make([]ModelCheckSupportedOption, 0, len(modelcheckprofile.SupportedModels()))
	for _, model := range modelcheckprofile.SupportedModels() {
		models = append(models, ModelCheckSupportedOption{Value: model, Label: model})
	}
	return ModelCheckOptions{
		SupportedModels: models,
		SupportedProfiles: []ModelCheckSupportedOption{
			{Value: "quick", Label: "快速检测", Description: "最多执行 2 个轻量串行探针，快速给出初步判断"},
			{Value: "full", Label: "深度检测", Description: "准确优先，执行多轮协议、行为指纹、长上下文、稳定性和可信对比探针"},
		},
		DefaultModel:   modelcheckprofile.DefaultModel,
		DefaultProfile: modelcheckprofile.DefaultProfile,
		TrustedComparison: map[string]any{
			"enabledByDefault": false,
			"available":        true,
			"message":          "可信对比默认关闭；选择一个你信任的可用同协议账户后，会额外消耗该账户额度",
		},
	}
}

// ListAccountOptions reads only accounts eligible for the requested purpose.
// Admin authorization is enforced by the HTTP layer; this method still keeps
// the query bounded and excludes deleted/un-grouped accounts.
func (s *BusinessTargetSource) ListAccountOptions(ctx context.Context, options AccountOptionsQuery) ([]AccountOption, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("J3b Business account options source is not initialized")
	}
	if options.Purpose != "run" && options.Purpose != "history" && options.Purpose != "schedule" {
		return nil, fmt.Errorf("J3b account options purpose is invalid")
	}
	limit := options.Limit
	if limit < 1 || limit > 50 {
		return nil, fmt.Errorf("J3b account options limit is invalid")
	}
	args := make([]any, 0, 8)
	where := []string{"a.deleted_at IS NULL", "p.enabled = " + s.boolLiteral(true)}
	if options.Purpose == "run" || options.Purpose == "schedule" {
		where = append(where, "a.status = 'active'", "a.schedulable = "+s.boolLiteral(true))
	}
	where = append(where, "EXISTS (SELECT 1 FROM "+s.table("group_accounts")+" ga JOIN "+s.table("groups")+" g ON g.id=ga.group_id WHERE ga.account_id=a.id AND ga.enabled="+s.boolLiteral(true)+" AND g.enabled="+s.boolLiteral(true)+")")
	if options.AccountID != "" {
		where = append(where, "a.id="+s.placeholder(len(args)+1))
		args = append(args, options.AccountID)
	} else {
		searchParts := make([]string, 0, 2)
		if options.Keyword != "" {
			searchParts = append(searchParts, "(LOWER(a.name) LIKE "+s.placeholder(len(args)+1)+" OR LOWER(a.id) LIKE "+s.placeholder(len(args)+2)+")")
			needle := "%" + strings.ToLower(options.Keyword) + "%"
			args = append(args, needle, needle)
		}
		if len(options.SelectedID) > 0 {
			placeholders := make([]string, len(options.SelectedID))
			for i, id := range options.SelectedID {
				placeholders[i] = s.placeholder(len(args) + i + 1)
				args = append(args, id)
			}
			searchParts = append(searchParts, "a.id IN ("+strings.Join(placeholders, ",")+")")
		}
		if len(searchParts) > 0 {
			where = append(where, "("+strings.Join(searchParts, " OR ")+")")
		}
	}
	query := "SELECT a.id,a.name,a.provider_code,a.provider_protocol_profile_id,a.protocol_code,a.protocol_version FROM " + s.table("accounts") + " a JOIN " + s.table("provider_protocol_profiles") + " p ON p.id=a.provider_protocol_profile_id WHERE " + strings.Join(where, " AND ") + " ORDER BY LOWER(a.name),a.id LIMIT " + s.placeholder(len(args)+1)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read J3b account options: %w", err)
	}
	defer rows.Close()
	result := make([]AccountOption, 0, limit)
	for rows.Next() {
		var option AccountOption
		if err := rows.Scan(&option.ID, &option.Name, &option.ProviderCode, &option.ProviderProtocolProfile, &option.ProtocolCode, &option.ProtocolVersion); err != nil {
			return nil, fmt.Errorf("scan J3b account option: %w", err)
		}
		if profile, ok := modelcheckprofile.Find(option.ProviderCode, option.ProviderProtocolProfile); ok {
			option.ModelCheckModels = append([]string(nil), profile.Models...)
		}
		result = append(result, option)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate J3b account options: %w", err)
	}
	if options.AccountID != "" && len(result) > 1 {
		sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	}
	return result, nil
}

func (s *BusinessTargetSource) boolLiteral(value bool) string {
	if s.postgres {
		if value {
			return "TRUE"
		}
		return "FALSE"
	}
	if value {
		return "1"
	}
	return "0"
}
