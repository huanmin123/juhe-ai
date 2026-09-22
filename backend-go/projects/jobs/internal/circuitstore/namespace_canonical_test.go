package circuitstore

import (
	"testing"

	"github.com/redis/go-redis/v9"
)

// namespace 入口 canonical 化回归（2026-09-22 对齐 gateway 加载层）：全前缀
// 配置（`juhe-ai:dev-space`）在各组件构造入口剥除 `juhe-ai:` 根前缀，与短名
// 落同一键空间；短名输入逐字节不变。形状校验仍由装配层对原始值 fail closed。
func TestNamespaceEntryCanonicalization(t *testing.T) {
	const full = "juhe-ai:dev-space"
	const short = "dev-space"

	newClient := func() redis.Cmdable {
		// 构造期只存句柄不建连（go-redis 惰性连接），关闭由 t.Cleanup 兜底。
		client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
		t.Cleanup(func() { _ = client.Close() })
		return client
	}

	t.Run("NewRedisStore", func(t *testing.T) {
		fullStore, err := NewRedisStore(RedisStoreOptions{Client: newClient(), Namespace: full, Capacity: 4})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = fullStore.Close() })
		shortStore, err := NewRedisStore(RedisStoreOptions{Client: newClient(), Namespace: short, Capacity: 4})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = shortStore.Close() })
		if fullStore.keys != shortStore.keys {
			t.Fatalf("全前缀与短名键空间不一致: %#v vs %#v", fullStore.keys, shortStore.keys)
		}
		if want := "juhe-ai:dev-space:account-circuit:gateway-account-circuit:states"; fullStore.keys.states != want {
			t.Fatalf("短名键空间被改写: %s want %s", fullStore.keys.states, want)
		}
	})

	t.Run("NewOverlayRedisStore", func(t *testing.T) {
		fullStore, err := NewOverlayRedisStore(OverlayRedisConfig{Namespace: full}, newClient())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = fullStore.Close() })
		shortStore, err := NewOverlayRedisStore(OverlayRedisConfig{Namespace: short}, newClient())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = shortStore.Close() })
		probe := "juhe-ai:account-concurrency-v2:acc-1:total"
		if got, want := fullStore.namespaced(probe), "juhe-ai:dev-space:account-concurrency-v2:acc-1:total"; got != want {
			t.Fatalf("全前缀 overlay 键=%q want %q", got, want)
		}
		if fullStore.namespaced(probe) != shortStore.namespaced(probe) {
			t.Fatalf("全前缀与短名 overlay 键空间不一致: %q vs %q", fullStore.namespaced(probe), shortStore.namespaced(probe))
		}
	})

	t.Run("NewProbeStateStore", func(t *testing.T) {
		client := newClient()
		fullStore, err := NewProbeStateStore("", full, client)
		if err != nil {
			t.Fatal(err)
		}
		shortStore, err := NewProbeStateStore("", short, client)
		if err != nil {
			t.Fatal(err)
		}
		if fullStore.namespace != shortStore.namespace || fullStore.statePrefix != shortStore.statePrefix ||
			fullStore.generationPrefix != shortStore.generationPrefix || fullStore.dueKey != shortStore.dueKey {
			t.Fatalf("全前缀与短名探针键空间不一致: %#v vs %#v", *fullStore, *shortStore)
		}
		if want := "juhe-ai:dev-space:probe:" + ProbeStoreName + ":state:"; fullStore.statePrefix != want {
			t.Fatalf("短名探针键前缀被改写: %s want %s", fullStore.statePrefix, want)
		}
	})

	t.Run("NewRuntimeStateReader", func(t *testing.T) {
		client := newClient()
		fullStore, err := NewRuntimeStateReader("", full, client)
		if err != nil {
			t.Fatal(err)
		}
		shortStore, err := NewRuntimeStateReader("", short, client)
		if err != nil {
			t.Fatal(err)
		}
		if fullStore.statePrefix != shortStore.statePrefix || fullStore.dueKey != shortStore.dueKey ||
			fullStore.avoidanceKeyPrefix != shortStore.avoidanceKeyPrefix {
			t.Fatalf("全前缀与短名运行态键空间不一致: %#v vs %#v", *fullStore, *shortStore)
		}
		if want := "juhe-ai:dev-space:probe:" + recoveryProbeStoreName + ":state:"; fullStore.statePrefix != want {
			t.Fatalf("短名运行态键前缀被改写: %s want %s", fullStore.statePrefix, want)
		}
	})
}
