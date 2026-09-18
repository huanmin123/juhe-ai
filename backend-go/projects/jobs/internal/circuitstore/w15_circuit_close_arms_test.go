package circuitstore

// w15_circuit_close_arms_test.go 补齐 Close 的非 *redis.Client 分支与
// encodeJSON 的防御 panic 分支。不连接真实 Redis（类型化 nil 注入），无
// 任何共享状态。

import (
	"testing"

	redis "github.com/redis/go-redis/v9"
)

func TestW15OverlayRedisStoreCloseNonClientCmdable(t *testing.T) {
	store, err := NewOverlayRedisStore(OverlayRedisConfig{Namespace: "w15"}, (*redis.Client)(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close 对非 *redis.Client 注入必须走保守 no-op 臂: %v", err)
	}
}

func TestW15ProbeStateStoreCloseNonClientCmdable(t *testing.T) {
	store, err := NewProbeStateStore("", "w15", (*redis.Client)(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close 对非 *redis.Client 注入必须走保守 no-op 臂: %v", err)
	}
}

func TestW15RuntimeStateReaderCloseNonClientCmdable(t *testing.T) {
	reader, err := NewRuntimeStateReader("", "w15", (*redis.Client)(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close 对非 *redis.Client 注入必须走保守 no-op 臂: %v", err)
	}
}

func TestW15EncodeJSONPanicsOnUnserializablePayload(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("encodeJSON 对不可序列化载荷必须 panic（内部载荷序列化失败属编程错误）")
		}
	}()
	encodeJSON(map[string]any{"unserializable": func() {}})
}
