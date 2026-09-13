package gatewayproxyhealth

import (
	"context"
	"errors"
	"testing"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// Redis 数值归一化：go-redis 不同返回形态（整数/浮点/字符串/布尔/垃圾）的容错契约。
func TestWhNumericRedisResult(t *testing.T) {
	cases := []struct {
		input any
		want  int64
	}{
		{int64(7), 7},
		{7, 7},
		{7.9, 7},
		{true, 1},
		{false, 0},
		{"9", 9},
		{" 9 ", 9},
		{"bad", 0},
		{nil, 0},
		{struct{}{}, 0},
	}
	for _, c := range cases {
		if got := numericRedisResult(c.input); got != c.want {
			t.Fatalf("numericRedisResult(%#v) = %d, want %d", c.input, got, c.want)
		}
	}
	// 非数组返回一律视为 nil 数组。
	if got := numericRedisArray("not-array"); got != nil {
		t.Fatalf("非数组返回 = %v", got)
	}
	if got := numericRedisArray([]any{int64(1), "2", 3.9}); len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("数组归一化 = %v", got)
	}
	// 负数地板除（Lua tonumber 差值可能为负）。
	if floorDiv(-1, 2) != -1 || floorDiv(5, 2) != 2 || floorDiv(4, 2) != 2 {
		t.Fatal("floorDiv 语义错误")
	}
}

// Redis 存储错误必须原样传播且不吞掉状态。
func TestWhRedisRuntimeStateErrorPropagation(t *testing.T) {
	_, client := whRedisForTest(t)
	// 直接构造错误注入的命令面。
	store := &RedisRuntimeStateStore{
		client: &whErrorRedis{inner: client, failAll: true},
		prefix: "juhe-ai:wh:state:err:",
	}
	ctx := contextBackground()
	if _, err := store.GetJSON(ctx, "k"); err == nil {
		t.Fatal("Get 故障必须传播")
	}
	if _, err := store.GetJSONMany(ctx, []string{"k"}); err == nil {
		t.Fatal("MGet 故障必须传播")
	}
	if err := store.SetJSON(ctx, "k", map[string]any{"a": 1}, 1000); err == nil {
		t.Fatal("Set 故障必须传播")
	}
	if _, err := store.CompareSetJSON(ctx, "k", nil, map[string]any{"a": 1}, 1000); err == nil {
		t.Fatal("CAS 故障必须传播")
	}
	if _, err := store.CompareDeleteJSON(ctx, "k", []byte("{}")); err == nil {
		t.Fatal("CompareDelete 故障必须传播")
	}
	if err := store.Delete(ctx, "k"); err == nil {
		t.Fatal("Del 故障必须传播")
	}
	if _, err := store.AcquireLock(ctx, "k", 1000, "t"); err == nil {
		t.Fatal("SetNX 故障必须传播")
	}
	if _, err := store.RenewLock(ctx, "k", 1000, "t"); err == nil {
		t.Fatal("Eval 故障必须传播")
	}
	if err := store.ReleaseLock(ctx, "k", "t"); err == nil {
		t.Fatal("释放锁 Eval 故障必须传播")
	}
}

// whErrorRedis 全命令失败的 redisStateClient 替身。
type whErrorRedis struct {
	inner    redisStateClient
	failAll  bool
	injected error
}

func (c *whErrorRedis) err() error {
	if c.injected != nil {
		return c.injected
	}
	return errors.New("wh 注入 Redis 故障")
}

func (c *whErrorRedis) Get(ctx context.Context, key string) *redis.StringCmd {
	cmd := redis.NewStringCmd(ctx)
	if c.failAll {
		cmd.SetErr(c.err())
	}
	return cmd
}

func (c *whErrorRedis) Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd {
	cmd := redis.NewStatusCmd(ctx)
	if c.failAll {
		cmd.SetErr(c.err())
	}
	return cmd
}

func (c *whErrorRedis) SetNX(ctx context.Context, key string, value any, expiration time.Duration) *redis.BoolCmd {
	cmd := redis.NewBoolCmd(ctx)
	if c.failAll {
		cmd.SetErr(c.err())
	}
	return cmd
}

func (c *whErrorRedis) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	cmd := redis.NewIntCmd(ctx)
	if c.failAll {
		cmd.SetErr(c.err())
	}
	return cmd
}

func (c *whErrorRedis) MGet(ctx context.Context, keys ...string) *redis.SliceCmd {
	cmd := redis.NewSliceCmd(ctx)
	if c.failAll {
		cmd.SetErr(c.err())
	}
	return cmd
}

func (c *whErrorRedis) Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd {
	cmd := redis.NewCmd(ctx)
	if c.failAll {
		cmd.SetErr(c.err())
	}
	return cmd
}
