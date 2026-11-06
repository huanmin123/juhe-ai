package crossprocessstate

// 真实 Redis 的批量版本读取基准（opt-in，JUHE_AI_TEST_REDIS_URL）。用于
// 评估 gateway 每次 runtime 缓存命中的合并 MGET 随候选账户数增长的开销；
// 只读/写本基准唯一 namespace，结束删除键，不执行 FLUSH。

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

func BenchmarkVersionsMultiRealRedis(b *testing.B) {
	rawURL := strings.TrimSpace(os.Getenv("JUHE_AI_TEST_REDIS_URL"))
	if rawURL == "" {
		b.Skip("requires JUHE_AI_TEST_REDIS_URL=redis://host:port/0")
	}
	client, err := rediscfg.NewUniversalClient(rediscfg.Config{URL: rawURL, ContextTimeoutEnabled: true})
	if err != nil {
		b.Fatalf("NewUniversalClient: %v", err)
	}
	defer client.Close()
	namespace := rediscfg.DomainNamespace("juhe-ai:cptest-bench-"+time.Now().Format("150405.000000000"), rediscfg.DomainOAuth)
	state, err := NewRedis(client, namespace)
	if err != nil {
		b.Fatalf("NewRedis: %v", err)
	}
	ctx := context.Background()
	for _, size := range []int{100, 500, 1000} {
		refs := make([]VersionRef, 0, size)
		keys := make([]string, 0, size)
		for index := 0; index < size; index++ {
			id := fmt.Sprintf("acc-%d", index)
			ref := VersionRef{Entity: EntityGatewayStateVersion, ID: "account:" + id}
			refs = append(refs, ref)
			keys = append(keys, state.Key(ref.Entity, ref.ID))
		}
		if err := client.MSet(ctx, versionPairs(keys)...).Err(); err != nil {
			b.Fatalf("MSet: %v", err)
		}
		b.Run(fmt.Sprintf("keys=%d", size), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				versions, err := state.VersionsMulti(ctx, refs)
				if err != nil || len(versions) != size {
					b.Fatalf("VersionsMulti len=%d err=%v", len(versions), err)
				}
			}
		})
		defer client.Del(ctx, keys...)
	}
}

func versionPairs(keys []string) []any {
	pairs := make([]any, 0, len(keys)*2)
	for _, key := range keys {
		pairs = append(pairs, key, "1")
	}
	return pairs
}
