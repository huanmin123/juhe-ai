package gatewayruntimecache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// ---------------------------------------------------------------------------
// internal gateway registry（注册/注销/枚举，miniredis 闭环）
// ---------------------------------------------------------------------------

func TestRegistryPublishListUnregister(t *testing.T) {
	mr := miniredis.RunT(t)
	redisURL := "redis://" + mr.Addr()

	publisherConfig := RegistryConfig{
		RedisURL: redisURL, Namespace: "dev", Secret: "registry-secret",
		InstanceID: "gw-1", Port: 65432,
		PublisherEnabled: true,
	}
	publisher, err := NewRegistry(publisherConfig)
	if err != nil {
		t.Fatalf("NewRegistry(publisher): %v", err)
	}
	defer publisher.Close()
	reader, err := NewRegistry(RegistryConfig{
		RedisURL: redisURL, Namespace: "dev", Secret: "registry-secret",
		InstanceID: "gw-1", Port: 65432,
		ReaderEnabled: true,
	})
	if err != nil {
		t.Fatalf("NewRegistry(reader): %v", err)
	}
	defer reader.Close()

	// 注册前为空。
	endpoints, err := reader.ListEndpoints(context.Background())
	if err != nil {
		t.Fatalf("empty list: %v", err)
	}
	if len(endpoints) != 0 {
		t.Fatalf("registry must start empty, got %v", endpoints)
	}

	publisher.Start()
	ctx := context.Background()
	waitFor(t, 5*time.Second, func() bool {
		endpoints, err = reader.ListEndpoints(ctx)
		if err != nil {
			t.Fatalf("list after publish: %v", err)
		}
		return len(endpoints) == 1
	})
	if endpoints[0].InstanceID != "gw-1" {
		t.Fatalf("instance id mismatch: %+v", endpoints[0])
	}
	if endpoints[0].Origin != "http://127.0.0.1:65432" {
		t.Fatalf("origin mismatch: %+v", endpoints[0])
	}

	// 注销：Stop 后心跳停止、条目删除。
	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := publisher.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		endpoints, err = reader.ListEndpoints(ctx)
		if err != nil {
			t.Fatalf("list after stop: %v", err)
		}
		return len(endpoints) == 0
	})
}

func TestRegistrySignatureValidation(t *testing.T) {
	mr := miniredis.RunT(t)
	redisURL := "redis://" + mr.Addr()
	publisher, err := NewRegistry(RegistryConfig{
		RedisURL: redisURL, Namespace: "dev", Secret: "secret-a",
		InstanceID: "gw-sig", Port: 65433, PublisherEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	reader, err := NewRegistry(RegistryConfig{
		RedisURL: redisURL, Namespace: "dev", Secret: "secret-b",
		InstanceID: "gw-sig", Port: 65433, ReaderEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	// 不同 secret：签名校验必须拒绝该条目。
	publisher.Start()
	time.Sleep(300 * time.Millisecond)
	endpoints, err := reader.ListEndpoints(context.Background())
	if err != nil {
		t.Fatalf("list with mismatched secret: %v", err)
	}
	if len(endpoints) != 0 {
		t.Fatalf("signature mismatch must reject the entry, got %v", endpoints)
	}
	_ = publisher.Stop(context.Background())
}

func TestRegistryReaderDisabledReturnsEmpty(t *testing.T) {
	registry, err := NewRegistry(RegistryConfig{
		RedisURL: "redis://127.0.0.1:1", Namespace: "dev", Secret: "s",
		InstanceID: "gw", Port: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	endpoints, err := registry.ListEndpoints(context.Background())
	if err != nil {
		t.Fatalf("disabled reader: %v", err)
	}
	if len(endpoints) != 0 {
		t.Fatalf("disabled reader must return empty, got %v", endpoints)
	}
}
