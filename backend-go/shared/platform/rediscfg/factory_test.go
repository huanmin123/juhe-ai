package rediscfg

import (
	"strings"
	"testing"

	redis "github.com/redis/go-redis/v9"
)

func TestParseMode(t *testing.T) {
	cases := []struct {
		in      string
		want    Mode
		wantErr bool
	}{
		{"", ModeSingle, false},
		{"single", ModeSingle, false},
		{" CLUSTER ", ModeCluster, false},
		{"sentinel", "", true},
	}
	for _, tc := range cases {
		got, err := ParseMode(tc.in)
		if (err != nil) != tc.wantErr {
			t.Fatalf("ParseMode(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
		}
		if err == nil && got != tc.want {
			t.Fatalf("ParseMode(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestNewUniversalClientSingleAndPasswordInjection(t *testing.T) {
	client, err := NewUniversalClient(Config{
		URL:      "redis://redis:6379",
		Mode:     ModeSingle,
		Password: "p@ss:word/1",
	})
	if err != nil {
		t.Fatalf("NewUniversalClient: %v", err)
	}
	defer client.Close()
	if _, ok := client.(*redis.Client); !ok {
		t.Fatalf("single mode client type = %T, want *redis.Client", client)
	}

	original, err := injectPassword("redis://redis:6379", "p@ss:word/1")
	if err != nil {
		t.Fatalf("injectPassword: %v", err)
	}
	opts, err := redis.ParseURL(original)
	if err != nil {
		t.Fatalf("ParseURL injected: %v", err)
	}
	if opts.Password != "p@ss:word/1" {
		t.Fatalf("injected password mismatch")
	}
	if opts.DB != 0 {
		t.Fatalf("single mode DB = %d, want 0", opts.DB)
	}
}

func TestNewUniversalClientHonorsURLPassword(t *testing.T) {
	injected, err := injectPassword("redis://:url-pass@redis:6379", "env-pass")
	if err != nil {
		t.Fatalf("injectPassword: %v", err)
	}
	if injected != "redis://:url-pass@redis:6379" {
		t.Fatalf("URL password must win, got %q", injected)
	}
}

func TestNewUniversalClientRejectsNonZeroDB(t *testing.T) {
	for _, mode := range []Mode{ModeSingle, ModeCluster} {
		if _, err := NewUniversalClient(Config{URL: "redis://redis:6379/1", Mode: mode}); err == nil {
			t.Fatalf("mode %s accepted DB1", mode)
		}
	}
}

func TestNewUniversalClientCluster(t *testing.T) {
	client, err := NewUniversalClient(Config{
		URL:      "redis://node1:6379?addr=node2:6379&addr=node3:6379",
		Mode:     ModeCluster,
		Password: "cluster-pass",
	})
	if err != nil {
		t.Fatalf("NewUniversalClient cluster: %v", err)
	}
	defer client.Close()
	if _, ok := client.(*redis.ClusterClient); !ok {
		t.Fatalf("cluster mode client type = %T, want *redis.ClusterClient", client)
	}
}

func TestDomainNamespace(t *testing.T) {
	if got, want := DomainNamespace("juhe-ai:prod", "circuit"), "prod:circuit"; got != want {
		t.Fatalf("DomainNamespace = %q, want %q", got, want)
	}
	if got, want := DomainNamespace("prod", ""), "prod"; got != want {
		t.Fatalf("DomainNamespace empty domain = %q, want %q", got, want)
	}
}

func TestTaggedNamespacedKey(t *testing.T) {
	cases := []struct{ key, ns, want string }{
		{"juhe-ai:account-circuit:states", "prod:circuit", "juhe-ai:{prod:circuit}:account-circuit:states"},
		{"account-circuit:states", "prod:circuit", "juhe-ai:{prod:circuit}:account-circuit:states"},
		{"juhe-ai:{prod:circuit}:states", "prod:circuit", "juhe-ai:{prod:circuit}:states"},
	}
	for _, tc := range cases {
		if got := TaggedNamespacedKey(tc.key, tc.ns); got != tc.want {
			t.Fatalf("TaggedNamespacedKey(%q,%q)=%q want %q", tc.key, tc.ns, got, tc.want)
		}
	}
	if !strings.Contains(TaggedNamespacedKey("x", "prod:cache"), "{prod:cache}") {
		t.Fatalf("tag missing")
	}
}

func TestNewUniversalClientUsesProcessConfiguration(t *testing.T) {
	Configure(Config{Mode: ModeCluster})
	t.Cleanup(func() { Configure(Config{}) })
	client, err := NewUniversalClient(Config{URL: "redis://127.0.0.1:7001"})
	if err != nil {
		t.Fatalf("cluster from process config: %v", err)
	}
	defer client.Close()
	if _, ok := client.(*redis.ClusterClient); !ok {
		t.Fatalf("process cluster client type = %T, want *redis.ClusterClient", client)
	}

	// 显式 Mode 优先于进程缺省。
	client, err = NewUniversalClient(Config{URL: "redis://127.0.0.1:6379", Mode: ModeSingle})
	if err != nil {
		t.Fatalf("explicit single: %v", err)
	}
	defer client.Close()
	if _, ok := client.(*redis.Client); !ok {
		t.Fatalf("explicit single client type = %T, want *redis.Client", client)
	}
}

func TestClusterClientFailureTimeoutsAreBounded(t *testing.T) {
	client, err := NewUniversalClient(Config{URL: "redis://127.0.0.1:7001", Mode: ModeCluster})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	cluster, ok := client.(*redis.ClusterClient)
	if !ok {
		t.Fatalf("client type = %T", client)
	}
	opts := cluster.Options()
	if opts.DialTimeout != clusterDialTimeout || opts.ReadTimeout != clusterIOTimeout ||
		opts.WriteTimeout != clusterIOTimeout || opts.PoolTimeout != clusterIOTimeout ||
		opts.MaxRetries != clusterMaxRetries {
		t.Fatalf("cluster timeouts not bounded: %+v", opts)
	}
}
