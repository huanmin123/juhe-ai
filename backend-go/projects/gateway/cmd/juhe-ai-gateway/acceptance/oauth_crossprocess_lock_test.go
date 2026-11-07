// X05 场景：OAuth 刷新跨进程锁 E2E。
//
// 复用 startGateway 的隔离 PG/Redis/namespace，直接播种一个到期 gpt OAuth
// 账户与代理 profile，然后由测试进程在共享 Redis 写入同一账户锁，验证两个
// 真实进程都遵守该锁：
//   - gateway 管理面 refresh-token 返回 409（账户正在刷新中），不调用 token
//     端点（锁在 token 交换前获取）；
//   - jobs openai-oauth-access-token-refresh 轮次把该账户计入 skippedLocked，
//     同样不调用 token 端点。
//
// 不访问真实上游：锁被占用时两条路径都在 token 交换前返回。
package acceptance

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/crossprocessstate"
	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

func TestAcceptanceOAuthRefreshCrossProcessLock(t *testing.T) {
	jobsBinary := ensureJobsBinary(t)
	fixture := startGateway(t, gatewayEnvOptions{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := sql.Open("pgx", fixture.dsn)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer db.Close()

	accountID := "acc-lock-e2e"
	proxyID := "proxy-lock-e2e"
	now := time.Now().UTC()
	nowText := now.Format("2006-01-02T15:04:05.000Z")
	expiredText := now.Add(-time.Hour).Format("2006-01-02T15:04:05.000Z")
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.proxy_profiles
		(id, system_account_id, name, type, host, port, enabled, created_at, updated_at)
		VALUES ($1, 'sys_admin', '锁 E2E 代理', 'http', '127.0.0.1', 1, TRUE, $2, $2)`,
		proxyID, now); err != nil {
		t.Fatalf("seed proxy profile: %v", err)
	}
	sealed := acceptanceOAuthCiphertext(t, fixture.secret, map[string]any{
		"access_token": "old-access-token", "refresh_token": "rt-e2e", "client_id": "cid-e2e",
	})
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.accounts
		(id, config_revision, system_account_id, vendor_code, connection_driver_code,
		 protocol_code, protocol_version, name, type, status, credentials_encrypted,
		 credential_mask, oauth_access_token_expires_at, oauth_refresh_token_present,
		 proxy_profile_id, created_at, updated_at)
		VALUES ($1, 1, 'sys_admin', 'gpt', 'gpt', 'openai', 'v1', '锁 E2E 账户', 'oauth',
		 'active', $2, '', $3, 1, $4, $5, $5)`,
		accountID, sealed, expiredText, proxyID, nowText); err != nil {
		t.Fatalf("seed oauth account: %v", err)
	}

	client, err := rediscfg.NewUniversalClient(rediscfg.Config{URL: fixture.redisURL})
	if err != nil {
		t.Fatalf("redis client: %v", err)
	}
	defer client.Close()
	lockKey := rediscfg.TaggedNamespacedKey(
		crossprocessstate.EntityOAuthRefreshLock+":"+accountID,
		rediscfg.DomainNamespace(fixture.redisNamespace, rediscfg.DomainOAuth),
	)
	if err := client.Set(ctx, lockKey, "acceptance-held-token", 5*time.Minute).Err(); err != nil {
		t.Fatalf("hold oauth lock: %v", err)
	}
	defer client.Del(context.Background(), lockKey)

	// gateway：锁被占用 → 409，且不等待/不调用 token 端点。
	httpClient := fixture.loginClient(t, "admin", acceptanceAdminPassword)
	code, payload := newClientWithHTTP(t, fixture.baseURL, httpClient).do(
		http.MethodPost,
		"/__aisys__/api/openai-oauth/accounts/"+accountID+"/refresh-token",
		map[string]any{"expectedConfigRevision": 1},
	)
	if code != http.StatusConflict {
		t.Fatalf("gateway refresh-token status=%d want 409 payload=%v", code, payload)
	}
	if message, _ := payload["message"].(string); !strings.Contains(message, "正在刷新中") {
		t.Fatalf("gateway 409 message=%q, want 包含“正在刷新中”", message)
	}
	if t.Failed() {
		return
	}

	// jobs：同一账户在锁被占用时计入 skippedLocked，不进入 token 交换。
	env := map[string]string{
		"JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS":        fmt.Sprintf("127.0.0.1:%d", freePort(t)),
		"JUHE_AI_POSTGRES_URL":                      fixture.dsn,
		"JUHE_AI_RUNTIME_LOG_POSTGRES_URL":          fixture.dsn,
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL": fixture.dsn,
		"JUHE_AI_REDIS_URL":                         fixture.redisURL,
		"JUHE_AI_REDIS_NAMESPACE":                   fixture.redisNamespace,
		"JUHE_AI_DATA_DIR":                          filepath.Join(fixture.root, "storage"),
		"JUHE_AI_USAGE_SPOOL_DIRECTORY":             fixture.spoolDir,
		"JUHE_AI_INSTANCE_ID":                       "acceptance-jobs-lock",
		"JUHE_AI_WORKER_ROLE":                       "worker",
		"JUHE_AI_WORKER_REPLICA_INDEX":              "0",
		"JUHE_AI_SECRET":                            fixture.secret,
		"JUHE_AI_RUNTIME_LOG_INSTANCE_ID":           "acceptance-jobs-lock",
		"JUHE_AI_LOG_DIR":                           fixture.storage["logs"],
		"JUHE_AI_JOBS_DRAIN_TIMEOUT_MS":             "5000",
	}
	jobsProcess := startProcess(t, "jobs-lock", jobsBinary, envMapToSlice(env))
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		if logContains(jobsProcess.logPath, `"event":"oauth_refresh_round_completed"`, `"skippedLocked":1`) {
			return
		}
		if jobsProcess.cmd.ProcessState != nil {
			t.Fatalf("jobs exited before observing skippedLocked:\n%s", logTail(jobsProcess.logPath, 4096))
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("jobs 未在期限内把账户计入 skippedLocked:\n%s", logTail(jobsProcess.logPath, 4096))
}

// acceptanceOAuthCiphertext 复刻 oauthmgmt/accounts 的 v1 AES-256-GCM 封套
// （sha256(secret) 密钥、12 字节 IV、raw base64url），用于直接播种账户行。
func acceptanceOAuthCiphertext(t *testing.T, secret string, credentials map[string]any) string {
	t.Helper()
	plain, err := json.Marshal(credentials)
	if err != nil {
		t.Fatalf("marshal credentials: %v", err)
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		t.Fatalf("iv: %v", err)
	}
	sealed := gcm.Seal(nil, iv, plain, nil)
	ciphertext, tag := sealed[:len(sealed)-gcm.Overhead()], sealed[len(sealed)-gcm.Overhead():]
	encode := base64.RawURLEncoding.EncodeToString
	return strings.Join([]string{"v1", encode(iv), encode(tag), encode(ciphertext)}, ":")
}

// newClientWithHTTP 用既有会话 client 构造 acceptanceClient。
func newClientWithHTTP(t *testing.T, baseURL string, httpClient *http.Client) *acceptanceClient {
	t.Helper()
	return &acceptanceClient{t: t, http: httpClient, baseURL: baseURL}
}
