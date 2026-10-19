package accountscore

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
)

// Credentials mirrors AccountCredentials: an open record of credential fields.
type Credentials map[string]any

// ModelMapping mirrors AccountModelMapping. 供应商解耦 B4 W2（设计 §5.1/§8）：
// TargetConnectionPresetCode 是账户内自然键——连接 ID 永不由客户端提交，写侧
// 按 (账户, preset) 解析 account_connections.id 后写
// account_entry_mappings.target_connection_id；缺省/未知/停用 preset 按
// mapping_target_unsupported（400 族）显式拒绝，不造兼容层。UpstreamModel 语义
// 即 entryMappings[].targetModelId 的入参载体（按 models.upstream_model 身份键
// 解析为 target_model_id）。
type ModelMapping struct {
	SourceModel                string `json:"sourceModel"`
	SourceEndpointFamily       string `json:"sourceEndpointFamily"`
	UpstreamModel              string `json:"upstreamModel"`
	UpstreamEndpointFamily     string `json:"upstreamEndpointFamily"`
	TargetConnectionPresetCode string `json:"targetConnectionPresetCode"`
	Enabled                    *bool  `json:"enabled,omitempty"`
}

// AuthorizedAccountReader is the narrow cross-package port of the authz
// slice's authorized-instance projection
// (authz.Store.AuthorizedReadableAccountIDs). It maps the readable
// authorization instance account ids for one viewer.
type AuthorizedAccountReader interface {
	AuthorizedReadableAccountIDs(ctx context.Context, viewerSystemAccountID string) (map[string]bool, error)
}

// EncryptJSON delegates to the shared platform accountcrypto envelope (the
// single AES-256-GCM v1 copy shared with the apikeys slice and jobs
// oauthrefresh). Existing Node accounts.credentials_encrypted rows decrypt
// byte-for-byte through DecryptJSON.
func EncryptJSON(secret string, value any) (string, error) {
	return accountcrypto.EncryptJSON(secret, value)
}

// DecryptJSON delegates to the shared platform accountcrypto envelope: only
// the v1 envelope is accepted and the GCM tag is verified before JSON
// decoding.
func DecryptJSON(secret string, envelope string, target any) error {
	return accountcrypto.DecryptJSON(secret, envelope, target)
}
