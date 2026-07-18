package publicapi

import "net/http"

const Prefix = "/__aipublic__"

const (
	AuthTypeBearer       = "Bearer"
	JSONBodyLimitBytes   = 256 * 1024
	TokenValuePrefix     = "juis_"
	SourceStatusActive   = "active"
	SourceStatusDisabled = "disabled"
	TokenStatusActive    = "active"
	TokenStatusDisabled  = "disabled"
	TokenStatusRevoked   = "revoked"
)

const (
	ScopeAPIKeyListRead           = "juhe_ai_public:api_key_list:read"
	ScopeAPIKeyAddWrite           = "juhe_ai_public:api_key_add:write"
	ScopeAPIKeyUpdateWrite        = "juhe_ai_public:api_key_update:write"
	ScopeAPIKeyDeleteWrite        = "juhe_ai_public:api_key_delete:write"
	ScopeRouteStrategyListRead    = "juhe_ai_public:route_strategy_list:read"
	ScopeRouteStrategyAddWrite    = "juhe_ai_public:route_strategy_add:write"
	ScopeRouteStrategyUpdateWrite = "juhe_ai_public:route_strategy_update:write"
	ScopeRouteStrategyDeleteWrite = "juhe_ai_public:route_strategy_delete:write"
	ScopeGroupListRead            = "juhe_ai_public:group_list:read"
	ScopeGroupAddWrite            = "juhe_ai_public:group_add:write"
	ScopeGroupUpdateWrite         = "juhe_ai_public:group_update:write"
	ScopeGroupDeleteWrite         = "juhe_ai_public:group_delete:write"
	ScopeAccountListRead          = "juhe_ai_public:account_list:read"
	ScopeAccountAddWrite          = "juhe_ai_public:account_add:write"
	ScopeAccountUpdateWrite       = "juhe_ai_public:account_update:write"
	ScopeAccountDeleteWrite       = "juhe_ai_public:account_delete:write"
)

const (
	BuiltInTestSourceID               = "extsrc_builtin_test"
	BuiltInTestTokenID                = "exttok_builtin_test"
	BuiltInTestRateLimitWindowSeconds = 60
	BuiltInTestRateLimitMaxRequests   = 10
)

type Endpoint struct {
	ID     string
	Method string
	Path   string
	Scope  string
}

type ScopeOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

var endpoints = []Endpoint{
	{ID: "api-key-list", Method: http.MethodGet, Path: Prefix + "/api-key/list", Scope: ScopeAPIKeyListRead},
	{ID: "api-key-add", Method: http.MethodPost, Path: Prefix + "/api-key/add", Scope: ScopeAPIKeyAddWrite},
	{ID: "api-key-update", Method: http.MethodPost, Path: Prefix + "/api-key/update", Scope: ScopeAPIKeyUpdateWrite},
	{ID: "api-key-delete", Method: http.MethodPost, Path: Prefix + "/api-key/del", Scope: ScopeAPIKeyDeleteWrite},
	{ID: "route-strategy-list", Method: http.MethodGet, Path: Prefix + "/route-strategy/list", Scope: ScopeRouteStrategyListRead},
	{ID: "route-strategy-add", Method: http.MethodPost, Path: Prefix + "/route-strategy/add", Scope: ScopeRouteStrategyAddWrite},
	{ID: "route-strategy-update", Method: http.MethodPost, Path: Prefix + "/route-strategy/update", Scope: ScopeRouteStrategyUpdateWrite},
	{ID: "route-strategy-delete", Method: http.MethodPost, Path: Prefix + "/route-strategy/del", Scope: ScopeRouteStrategyDeleteWrite},
	{ID: "group-list", Method: http.MethodGet, Path: Prefix + "/group/list", Scope: ScopeGroupListRead},
	{ID: "group-add", Method: http.MethodPost, Path: Prefix + "/group/add", Scope: ScopeGroupAddWrite},
	{ID: "group-update", Method: http.MethodPost, Path: Prefix + "/group/update", Scope: ScopeGroupUpdateWrite},
	{ID: "group-delete", Method: http.MethodPost, Path: Prefix + "/group/del", Scope: ScopeGroupDeleteWrite},
	{ID: "account-list", Method: http.MethodGet, Path: Prefix + "/account/list", Scope: ScopeAccountListRead},
	{ID: "account-add", Method: http.MethodPost, Path: Prefix + "/account/add", Scope: ScopeAccountAddWrite},
	{ID: "account-update", Method: http.MethodPost, Path: Prefix + "/account/update", Scope: ScopeAccountUpdateWrite},
	{ID: "account-delete", Method: http.MethodPost, Path: Prefix + "/account/del", Scope: ScopeAccountDeleteWrite},
}

var scopeOptions = []ScopeOption{
	{Value: ScopeAPIKeyListRead, Label: "GET API Key 列表"},
	{Value: ScopeRouteStrategyListRead, Label: "GET 路由策略列表"},
	{Value: ScopeGroupListRead, Label: "GET 分组列表"},
	{Value: ScopeAccountListRead, Label: "GET 账号列表"},
	{Value: ScopeAPIKeyAddWrite, Label: "POST API Key 新增"},
	{Value: ScopeAPIKeyUpdateWrite, Label: "POST API Key 修改"},
	{Value: ScopeAPIKeyDeleteWrite, Label: "POST API Key 删除"},
	{Value: ScopeRouteStrategyAddWrite, Label: "POST 路由策略新增"},
	{Value: ScopeRouteStrategyUpdateWrite, Label: "POST 路由策略修改"},
	{Value: ScopeRouteStrategyDeleteWrite, Label: "POST 路由策略删除"},
	{Value: ScopeGroupAddWrite, Label: "POST 分组新增"},
	{Value: ScopeGroupUpdateWrite, Label: "POST 分组修改"},
	{Value: ScopeGroupDeleteWrite, Label: "POST 分组删除"},
	{Value: ScopeAccountAddWrite, Label: "POST 账号新增"},
	{Value: ScopeAccountUpdateWrite, Label: "POST 账号修改"},
	{Value: ScopeAccountDeleteWrite, Label: "POST 账号删除"},
}

func Endpoints() []Endpoint {
	out := make([]Endpoint, len(endpoints))
	copy(out, endpoints)
	return out
}

func ScopeOptions() []ScopeOption {
	out := make([]ScopeOption, len(scopeOptions))
	copy(out, scopeOptions)
	return out
}

func FindEndpoint(method string, path string) (Endpoint, bool) {
	for _, endpoint := range endpoints {
		if endpoint.Method == method && endpoint.Path == path {
			return endpoint, true
		}
	}
	return Endpoint{}, false
}

func IsBuiltInTestSource(sourceRefID string) bool {
	return sourceRefID == BuiltInTestSourceID
}

func IsBuiltInTestToken(tokenID string) bool {
	return tokenID == BuiltInTestTokenID
}
