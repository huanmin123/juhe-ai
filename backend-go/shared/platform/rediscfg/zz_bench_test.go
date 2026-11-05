package rediscfg

import (
	"context"
	"os"
	"testing"
	"time"
)

func benchClient(b *testing.B, mode Mode, url, password, key string) {
	client, err := NewUniversalClient(Config{URL: url, Mode: mode, Password: password})
	if err != nil {
		b.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()
	if err := client.Set(ctx, key, "v", time.Minute).Err(); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := client.Set(ctx, key, "v", time.Minute).Err(); err != nil {
			b.Fatal(err)
		}
		if _, err := client.Get(ctx, key).Result(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkZZSingleSETGET(b *testing.B) {
	url := os.Getenv("JUHE_AI_ZZ_SINGLE_URL")
	if url == "" {
		b.Skip("no single url")
	}
	benchClient(b, ModeSingle, url, os.Getenv("JUHE_AI_ZZ_SINGLE_PASSWORD"), TaggedNamespacedKey("bench", DomainNamespace("dev", DomainKeyModel)))
}

func BenchmarkZZClusterSETGET(b *testing.B) {
	url := os.Getenv("JUHE_AI_ZZ_CLUSTER_URL")
	if url == "" {
		b.Skip("no cluster url")
	}
	benchClient(b, ModeCluster, url, os.Getenv("JUHE_AI_ZZ_CLUSTER_PASSWORD"), TaggedNamespacedKey("bench", DomainNamespace("dev", DomainKeyModel)))
}
