// secret KCV（升级兼容与迁移账本设计 §9.4-8/§9.4-11）：AES-256-GCM 无密钥
// 校验值，错误 secret 只会在 seed/AES 步骤后置失败——本检查在确认门禁之后、
// 任何写入之前，取库内任一现有密文用本次 secret 试解密，把「secret 与库内
// 密文不一致」前置为可人工处置的 blocked（KCV 只读，不产生写入）。
package upgradepipeline

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
)

// secretKCVProbes 是 KCV 密文载体的查询顺序：api_keys 默认 Key（seed 后任何
// 库都有）优先，accounts 凭据（存量库主载体）兜底。两列均为 v1 信封
// （accountcrypto 唯一实现）。
var secretKCVProbes = []struct {
	name  string
	query string
}{
	{
		name:  "api_keys.key_secret_encrypted",
		query: `SELECT key_secret_encrypted FROM juhe_business.api_keys WHERE COALESCE(key_secret_encrypted, '') <> '' LIMIT 1`,
	},
	{
		name:  "accounts.credentials_encrypted",
		query: `SELECT credentials_encrypted FROM juhe_business.accounts WHERE COALESCE(credentials_encrypted, '') <> '' LIMIT 1`,
	},
}

// verifySecretAgainstDatabase 取库内任一现有密文试解密。返回：
//   - (true, nil)：任一载体解密通过，secret 与库内密文一致；
//   - (false, nil)：库内无可试解密载体（fresh 库/无密文行），与 fresh 同口径
//     跳过 KCV，由报告披露 secretVerified=false（§20.5 备份兜底）；
//   - error：载体解密失败（secret 不一致或信封损坏），由 Run 转为 blocked。
func verifySecretAgainstDatabase(ctx context.Context, db *sql.DB, secret string) (bool, error) {
	for _, probe := range secretKCVProbes {
		var envelope string
		err := db.QueryRowContext(ctx, probe.query).Scan(&envelope)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("KCV 读取密文载体（%s）失败: %w", probe.name, err)
		}
		if err := accountcrypto.DecryptJSON(secret, envelope, &struct{}{}); err != nil {
			return false, fmt.Errorf(
				"secret 与库内密文不一致（KCV 载体 %s）: %v；请核对 --secret/JUHE_AI_SECRET 与库内密文使用的密钥",
				probe.name, err)
		}
		return true, nil
	}
	return false, nil
}
