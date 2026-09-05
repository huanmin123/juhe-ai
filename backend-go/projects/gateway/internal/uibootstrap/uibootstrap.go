// Package uibootstrap ports the ui-bootstrap route family (Node
// backend/src/modules/ui-bootstrap/ui-bootstrap.routes.ts +
// storage/user-reference-data.repository.ts): the per-user reference data
// options (provider defaults, default groups and route strategies) mounted on
// both the admin /ui-bootstrap prefix and the forceSelfAccessScope
// /my-ui-bootstrap prefix.
package uibootstrap

import (
	"context"
	"database/sql"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// gptVendorCode mirrors GPT_VENDOR_CODE.
const gptVendorCode = "gpt"

// Deps bundles the ui-bootstrap collaborators.
type Deps struct {
	DB        *sql.DB
	PGDialect bool
	Auth      *authsys.Deps
}

// Mount wires the ui-bootstrap surfaces:
//
//	GET /__aisys__/api/ui-bootstrap/options       (requireAdmin)
//	GET /__aisys__/api/my-ui-bootstrap/options    (forceSelfAccessScope)
func (d *Deps) Mount(k *kernel.Kernel) {
	prefix := "/__aisys__/api"
	k.Register("GET "+prefix+"/ui-bootstrap/options", d.Auth.RequireAdmin(http.HandlerFunc(d.options(false))))
	k.Register("GET "+prefix+"/my-ui-bootstrap/options", d.Auth.RequireSession(true)(http.HandlerFunc(d.options(true))))
}

func (d *Deps) options(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// parseRequestScopeQuery: an explicit blank systemAccountId query
		// value is a 400.
		if values := r.URL.Query()["systemAccountId"]; len(values) > 0 && strings.TrimSpace(values[0]) == "" {
			kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
			return
		}
		scope := requestScope(r)
		if selfOnly {
			scope = selfScope(r)
		}
		// Admin callers without a target scope are rejected (Node
		// ui-bootstrap.routes.ts): 请选择目标系统账户.
		if scope.IsAdmin && scope.FilterID == "" {
			kernel.WriteBadRequest(w, "请选择目标系统账户")
			return
		}
		systemAccountID := scope.scopedID()
		if systemAccountID == "" {
			systemAccountID = scope.ViewerID
		}
		reference, err := d.findUserReferenceData(r.Context(), systemAccountID)
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if reference == nil {
			kernel.WriteNotFound(w, "系统账户不存在")
			return
		}
		kernel.WriteOK(w, reference, "")
	}
}

type accessScope struct {
	ViewerID string
	IsAdmin  bool
	FilterID string
}

func (a accessScope) scopedID() string {
	if !a.IsAdmin {
		return a.ViewerID
	}
	return strings.TrimSpace(a.FilterID)
}

// requestScope mirrors getRequestAccessScope(query.systemAccountId).
func requestScope(r *http.Request) accessScope {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		return accessScope{}
	}
	if auth.Role != "admin" && auth.Role != "super_admin" {
		return accessScope{ViewerID: auth.SystemAccountID}
	}
	filter := strings.TrimSpace(r.URL.Query().Get("systemAccountId"))
	if filter == "all" {
		filter = ""
	}
	return accessScope{ViewerID: auth.SystemAccountID, IsAdmin: true, FilterID: filter}
}

// selfScope mirrors forceSelfAccessScope.
func selfScope(r *http.Request) accessScope {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		return accessScope{}
	}
	return accessScope{ViewerID: auth.SystemAccountID}
}

// userReferenceData mirrors UserReferenceData.
type userReferenceData struct {
	SystemAccountID               string                   `json:"systemAccountId"`
	ProviderDefaults              []userProviderDefault    `json:"providerDefaults"`
	PreferredDefaultRouteStrategy *defaultRouteStrategyRef `json:"preferredDefaultRouteStrategy,omitempty"`
}

type userProviderDefault struct {
	ProviderCode         string                   `json:"providerCode"`
	DefaultGroup         groupRef                 `json:"defaultGroup"`
	DefaultRouteStrategy *defaultRouteStrategyRef `json:"defaultRouteStrategy,omitempty"`
}

type groupRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type defaultRouteStrategyRef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Mode   string `json:"mode"`
	Status string `json:"status"`
}

func (d *Deps) table(name string) string {
	if d.PGDialect {
		return "juhe_business." + name
	}
	return name
}

// findUserReferenceData mirrors findUserReferenceDataForSystemAccountAsync.
func (d *Deps) findUserReferenceData(ctx context.Context, systemAccountID string) (*userReferenceData, error) {
	owner := strings.TrimSpace(systemAccountID)
	if owner == "" {
		return nil, nil
	}
	query := `
		SELECT
			system_accounts.id AS system_account_id,
			groups.provider_code,
			groups.id AS group_id,
			groups.name AS group_name,
			groups.enabled AS group_enabled,
			default_routes.route_strategy_id,
			default_routes.route_strategy_name,
			default_routes.route_strategy_mode,
			default_routes.route_strategy_status,
			default_routes.route_binding_status
		FROM ` + d.table("system_accounts") + ` system_accounts
		LEFT JOIN ` + d.table("groups") + ` groups
			ON groups.system_account_id = system_accounts.id
			AND groups.is_default = 1
		LEFT JOIN (
			SELECT
				route_strategy_groups.system_account_id,
				route_strategy_groups.group_id,
				route_strategy_groups.status AS route_binding_status,
				route_strategies.id AS route_strategy_id,
				route_strategies.name AS route_strategy_name,
				route_strategies.mode AS route_strategy_mode,
				route_strategies.status AS route_strategy_status,
				route_strategies.created_at AS route_strategy_created_at
			FROM ` + d.table("route_strategy_groups") + ` route_strategy_groups
			INNER JOIN ` + d.table("route_strategies") + ` route_strategies
				ON route_strategies.id = route_strategy_groups.route_strategy_id
				AND route_strategies.system_account_id = route_strategy_groups.system_account_id
				AND route_strategies.is_default = 1
		) default_routes
			ON default_routes.system_account_id = groups.system_account_id
			AND default_routes.group_id = groups.id
		WHERE system_accounts.id = ?
		ORDER BY
			groups.provider_code ASC,
			CASE WHEN default_routes.route_binding_status = 'active' THEN 0 ELSE 1 END ASC,
			CASE WHEN default_routes.route_strategy_status = 'active' THEN 0 ELSE 1 END ASC,
			default_routes.route_strategy_created_at ASC,
			default_routes.route_strategy_id ASC`
	rows, err := d.DB.QueryContext(ctx, query, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	reference := &userReferenceData{SystemAccountID: owner, ProviderDefaults: []userProviderDefault{}}
	providerDefaultsByCode := map[string]int{}
	sawRow := false
	for rows.Next() {
		sawRow = true
		var (
			systemAccountID string
			providerCode    sql.NullString
			groupID         sql.NullString
			groupName       sql.NullString
			groupEnabled    sql.NullBool
			strategyID      sql.NullString
			strategyName    sql.NullString
			strategyMode    sql.NullString
			strategyStatus  sql.NullString
			bindingStatus   sql.NullString
		)
		if err := rows.Scan(&systemAccountID, &providerCode, &groupID, &groupName, &groupEnabled,
			&strategyID, &strategyName, &strategyMode, &strategyStatus, &bindingStatus); err != nil {
			return nil, err
		}
		code := strings.TrimSpace(providerCode.String)
		id := strings.TrimSpace(groupID.String)
		name := strings.TrimSpace(groupName.String)
		if code == "" || id == "" || name == "" {
			continue
		}
		index, ok := providerDefaultsByCode[code]
		if !ok {
			reference.ProviderDefaults = append(reference.ProviderDefaults, userProviderDefault{
				ProviderCode: code,
				DefaultGroup: groupRef{ID: id, Name: name},
			})
			index = len(reference.ProviderDefaults) - 1
			providerDefaultsByCode[code] = index
		}
		providerDefault := &reference.ProviderDefaults[index]
		if strategy := routeStrategyRef(strategyID, strategyName, strategyMode, strategyStatus); strategy != nil && providerDefault.DefaultRouteStrategy == nil {
			providerDefault.DefaultRouteStrategy = strategy
		}
		if reference.PreferredDefaultRouteStrategy == nil &&
			code == gptVendorCode &&
			groupEnabled.Valid && groupEnabled.Bool &&
			bindingStatus.String == "active" {
			if strategy := routeStrategyRef(strategyID, strategyName, strategyMode, strategyStatus); strategy != nil && strategy.Status == "active" {
				reference.PreferredDefaultRouteStrategy = strategy
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !sawRow {
		return nil, nil
	}
	return reference, nil
}

// routeStrategyRef mirrors routeStrategyReferenceFromRow.
func routeStrategyRef(id, name, mode, status sql.NullString) *defaultRouteStrategyRef {
	idText := strings.TrimSpace(id.String)
	nameText := strings.TrimSpace(name.String)
	if idText == "" || nameText == "" || !mode.Valid || mode.String == "" || !status.Valid || status.String == "" {
		return nil
	}
	return &defaultRouteStrategyRef{ID: idText, Name: nameText, Mode: mode.String, Status: status.String}
}
