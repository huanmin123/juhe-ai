package realtimetoken

// realtimetoken 单元测试（miniredis + 真 go-redis 客户端，项目既有
// miniredis 测试先例）：签发/校验往返、TTL 过期、模型绑定、命名空间键位、
// 参数与存储故障臂。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newServiceOnMiniredis(t *testing.T, namespace string) (*Service, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewService(GoRedisClient{Client: client}, namespace), server
}

func TestIssueAndVerifyRoundTrip(t *testing.T) {
	service, _ := newServiceOnMiniredis(t, "")
	issued, err := service.Issue(context.Background(), "key_1", "gpt-realtime")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	// token 形态：base64url 无填充，32 字节熵 → 43 字符，可安全进查询参数。
	if len(issued.Value) != 43 || strings.ContainsAny(issued.Value, "+/=") {
		t.Fatalf("token shape = %q want 43-char base64url", issued.Value)
	}
	// expires_at = 签发时刻 + 120s（Unix 秒）。
	if issued.ExpiresAt <= time.Now().Add(TTL-time.Second).Unix() ||
		issued.ExpiresAt > time.Now().Add(TTL+time.Second).Unix() {
		t.Fatalf("expiresAt = %d want now+120s", issued.ExpiresAt)
	}
	// TTL 内多次校验都通过（浏览器重连场景）。
	for i := 0; i < 3; i++ {
		verified, err := service.Verify(context.Background(), issued.Value, "gpt-realtime")
		if err != nil {
			t.Fatalf("verify #%d: %v", i+1, err)
		}
		if verified.APIKeyID != "key_1" || verified.Model != "gpt-realtime" {
			t.Fatalf("verify payload = %+v", verified)
		}
	}
}

func TestVerifyModelBinding(t *testing.T) {
	service, _ := newServiceOnMiniredis(t, "")
	issued, err := service.Issue(context.Background(), "key_1", "gpt-realtime")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Verify(context.Background(), issued.Value, "gpt-4o"); !errors.Is(err, ErrModelMismatch) {
		t.Fatalf("cross-model verify err = %v want ErrModelMismatch", err)
	}
	// 原模型仍可校验（mismatch 不消耗 token）。
	if _, err := service.Verify(context.Background(), issued.Value, "gpt-realtime"); err != nil {
		t.Fatalf("original model verify after mismatch: %v", err)
	}
}

func TestVerifyExpiredToken(t *testing.T) {
	service, server := newServiceOnMiniredis(t, "")
	issued, err := service.Issue(context.Background(), "key_1", "gpt-realtime")
	if err != nil {
		t.Fatal(err)
	}
	// miniredis 快进越过 TTL：键消失，校验按无效拒绝。
	server.FastForward(TTL + time.Second)
	if _, err := service.Verify(context.Background(), issued.Value, "gpt-realtime"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired verify err = %v want ErrInvalidToken", err)
	}
	if _, err := service.Verify(context.Background(), "", "gpt-realtime"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("empty verify err = %v want ErrInvalidToken", err)
	}
}

func TestIssueRejectsEmptyParameters(t *testing.T) {
	service, _ := newServiceOnMiniredis(t, "")
	if _, err := service.Issue(context.Background(), "", "gpt-realtime"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty apiKeyID err = %v want ErrInvalidInput", err)
	}
	if _, err := service.Issue(context.Background(), "key_1", "  "); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("blank model err = %v want ErrInvalidInput", err)
	}
}

func TestKeyNamespacing(t *testing.T) {
	service, server := newServiceOnMiniredis(t, "juhe-ai:dev")
	issued, err := service.Issue(context.Background(), "key_1", "gpt-realtime")
	if err != nil {
		t.Fatal(err)
	}
	// 全前缀 namespace 被规范为短形式并插在 juhe-ai: 根后。
	fullKey := "juhe-ai:dev:" + keyPrefix + issued.Value
	if !server.Exists(fullKey) {
		t.Fatalf("namespaced key %s missing (keys: %v)", fullKey, server.Keys())
	}
	// TTL 落在键上（120s 窗口）。
	ttl := server.TTL(fullKey)
	if ttl <= 0 || ttl > TTL {
		t.Fatalf("ttl = %v want (0, 120s]", ttl)
	}
	// value 是 {api_key_id, model, created_at} JSON。
	value, err := server.Get(fullKey)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(value, `"api_key_id":"key_1"`) ||
		!strings.Contains(value, `"model":"gpt-realtime"`) ||
		!strings.Contains(value, `"created_at"`) {
		t.Fatalf("payload json = %s", value)
	}
}

func TestStoreUnavailableFailsClosed(t *testing.T) {
	// 客户端指向已关闭的 miniredis：签发/校验都必须显式失败。
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	service := NewService(GoRedisClient{Client: client}, "")
	server.Close()
	if _, err := service.Issue(context.Background(), "key_1", "gpt-realtime"); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("issue on dead store err = %v want ErrStoreUnavailable", err)
	}
	if _, err := service.Verify(context.Background(), "whatever", "gpt-realtime"); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("verify on dead store err = %v want ErrStoreUnavailable", err)
	}
}

func TestNilServiceFailsClosed(t *testing.T) {
	var service *Service
	if _, err := service.Issue(context.Background(), "key_1", "gpt-realtime"); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("nil service issue err = %v want ErrStoreUnavailable", err)
	}
	if _, err := service.Verify(context.Background(), "t", "gpt-realtime"); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("nil service verify err = %v want ErrStoreUnavailable", err)
	}
}

func TestTokenUniqueness(t *testing.T) {
	service, _ := newServiceOnMiniredis(t, "")
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		issued, err := service.Issue(context.Background(), "key_1", "gpt-realtime")
		if err != nil {
			t.Fatal(err)
		}
		if seen[issued.Value] {
			t.Fatalf("token collision at #%d: %s", i, issued.Value)
		}
		seen[issued.Value] = true
	}
}
