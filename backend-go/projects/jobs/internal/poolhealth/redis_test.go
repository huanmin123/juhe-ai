package poolhealth

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRedisHealthWriterWritesSharedKeyWithTTL(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	writer, err := NewRedisHealthWriter(client, "dev")
	if err != nil {
		t.Fatalf("NewRedisHealthWriter: %v", err)
	}
	ctx := context.Background()
	if err := writer.Write(ctx, "profile-1", HealthDead); err != nil {
		t.Fatalf("Write: %v", err)
	}
	key := "juhe-ai:{dev}:proxy-pool:member-health:profile-1"
	value, err := server.Get(key)
	if err != nil || value != "dead" {
		t.Fatalf("redis value = (%q, %v)", value, err)
	}
	if ttl := server.TTL(key); ttl <= 0 || ttl > 5*time.Minute {
		t.Fatalf("ttl = %v", ttl)
	}
	if err := writer.Delete(ctx, "profile-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if server.Exists(key) {
		t.Fatal("key must be deleted")
	}
}
