package rediscfg

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestZZClusterFailover(t *testing.T) {
	url := strings.TrimSpace(os.Getenv("JUHE_AI_ZZ_CLUSTER_URL"))
	password := os.Getenv("JUHE_AI_ZZ_CLUSTER_PASSWORD")
	if url == "" {
		t.Skip("no cluster url")
	}
	client, err := NewUniversalClient(Config{URL: url, Mode: ModeCluster, Password: password})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()
	ns := DomainNamespace("dev", DomainKeyModel)
	key := TaggedNamespacedKey("failover-probe", ns)
	if err := client.Set(ctx, key, "before", time.Minute).Err(); err != nil {
		t.Fatalf("initial set: %v", err)
	}
	start := time.Now()
	var errors int
	var firstErr time.Time
	var lastErr time.Time
	var okAfterFirstErr time.Time
	deadline := start.Add(25 * time.Second)
	for time.Now().Before(deadline) {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := client.Set(cctx, key, "during", time.Minute).Err()
		cancel()
		if err != nil {
			errors++
			if firstErr.IsZero() {
				firstErr = time.Now()
			}
			lastErr = time.Now()
		} else if !firstErr.IsZero() && okAfterFirstErr.IsZero() {
			okAfterFirstErr = time.Now()
		}
		time.Sleep(50 * time.Millisecond)
	}
	value, getErr := client.Get(ctx, key).Result()
	t.Logf("errors=%d firstErr=%s lastErr=%s recoveredAfter=%s final=%q getErr=%v",
		errors,
		firstErr.Sub(start).Round(time.Millisecond),
		lastErr.Sub(start).Round(time.Millisecond),
		okAfterFirstErr.Sub(firstErr).Round(time.Millisecond),
		value, getErr)
	if errors == 0 {
		t.Fatal("expected errors during failover")
	}
	if okAfterFirstErr.IsZero() {
		t.Fatal("client did not recover after failover")
	}
}
