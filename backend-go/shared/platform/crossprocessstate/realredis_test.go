package crossprocessstate

// 真实 Redis 集成测试（opt-in）：验证 SET NX PX / compare-delete / 单调版本 SET+Lua
// 在真实 go-redis 客户端上的行为。门控变量 JUHE_AI_TEST_REDIS_URL（与
// gateway acceptance harness 同名）；未设置时跳过。测试只操作本用例唯一
// namespace 下的锁/版本键并在结束时删除，不执行 FLUSH/SCAN 清理。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

func TestRealRedisLockAndVersionRoundTrip(t *testing.T) {
	rawURL := strings.TrimSpace(os.Getenv("JUHE_AI_TEST_REDIS_URL"))
	if rawURL == "" {
		t.Skip("requires JUHE_AI_TEST_REDIS_URL=redis://host:port/0")
	}
	client, err := rediscfg.NewUniversalClient(rediscfg.Config{URL: rawURL, ContextTimeoutEnabled: true})
	if err != nil {
		t.Fatalf("NewUniversalClient: %v", err)
	}
	defer client.Close()

	namespace := rediscfg.DomainNamespace("juhe-ai:cptest-"+time.Now().Format("150405.000000000"), rediscfg.DomainOAuth)
	state, err := NewRedis(client, namespace)
	if err != nil {
		t.Fatalf("NewRedis: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const accountID = "acc-real-1"
	lockKey := state.LockKey(EntityOAuthRefreshLock, accountID)
	versionKey := state.VersionKey(EntityAccountCredentialsVersion, accountID)
	defer client.Del(context.Background(), lockKey, versionKey)

	token, acquired, err := state.TryLock(ctx, EntityOAuthRefreshLock, accountID, time.Minute)
	if err != nil || !acquired || token == "" {
		t.Fatalf("首次 TryLock = (%q,%v,%v), want acquired", token, acquired, err)
	}
	if _, acquired, err := state.TryLock(ctx, EntityOAuthRefreshLock, accountID, time.Minute); err != nil || acquired {
		t.Fatalf("第二次 TryLock = acquired %v err %v, want busy", acquired, err)
	}
	if err := state.Unlock(ctx, EntityOAuthRefreshLock, accountID, "stale-token"); err != nil {
		t.Fatalf("错误 token Unlock: %v", err)
	}
	if _, acquired, err := state.TryLock(ctx, EntityOAuthRefreshLock, accountID, time.Minute); err != nil || acquired {
		t.Fatalf("错误 token 不应释放锁: acquired=%v err=%v", acquired, err)
	}
	if err := state.Unlock(ctx, EntityOAuthRefreshLock, accountID, token); err != nil {
		t.Fatalf("正确 token Unlock: %v", err)
	}
	if _, acquired, err := state.TryLock(ctx, EntityOAuthRefreshLock, accountID, time.Minute); err != nil || !acquired {
		t.Fatalf("释放后 TryLock = acquired %v err %v, want true", acquired, err)
	}

	for _, revision := range []int64{2, 3} {
		got, err := state.BumpVersion(ctx, EntityAccountCredentialsVersion, accountID, revision)
		if err != nil || got != revision {
			t.Fatalf("BumpVersion = (%d,%v), want (%d,nil)", got, err, revision)
		}
	}
	// 乱序回写不得让版本后退。
	if got, err := state.BumpVersion(ctx, EntityAccountCredentialsVersion, accountID, 1); err != nil || got != 3 {
		t.Fatalf("回退写 = (%d,%v), want (3,nil)", got, err)
	}
	versions, err := state.Versions(ctx, EntityAccountCredentialsVersion, []string{accountID, "acc-missing"})
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if versions[accountID] != 3 || versions["acc-missing"] != 0 {
		t.Fatalf("Versions = %v, want %s=3 missing=0", versions, accountID)
	}
	seqID := "account:" + accountID
	seqKey := state.Key(EntityGatewayStateVersion, seqID)
	defer client.Del(context.Background(), seqKey)
	for want := int64(1); want <= 2; want++ {
		got, err := state.BumpSeqVersion(ctx, EntityGatewayStateVersion, seqID)
		if err != nil || got != want {
			t.Fatalf("BumpSeqVersion = (%d,%v), want (%d,nil)", got, err, want)
		}
	}
}
