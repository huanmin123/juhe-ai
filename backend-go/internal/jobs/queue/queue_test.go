package queue

import (
	"context"
	"testing"
)

func TestParseRedisURL(t *testing.T) {
	opts, err := ParseRedisURL("redis://user:pass@127.0.0.1:6380/2")
	if err != nil {
		t.Fatalf("ParseRedisURL() error = %v", err)
	}
	if opts.Addr != "127.0.0.1:6380" || opts.Username != "user" || opts.Password != "pass" || opts.DB != 2 {
		t.Fatalf("unexpected options: %+v", opts)
	}
}

func TestParseRedisURLAddsDefaultPort(t *testing.T) {
	opts, err := ParseRedisURL("redis://127.0.0.1/0")
	if err != nil {
		t.Fatalf("ParseRedisURL() error = %v", err)
	}
	if opts.Addr != "127.0.0.1:6379" {
		t.Fatalf("Addr = %q", opts.Addr)
	}
}

func TestParseRedisURLSupportsIPv6(t *testing.T) {
	opts, err := ParseRedisURL("redis://[::1]:6380/3")
	if err != nil {
		t.Fatalf("ParseRedisURL() error = %v", err)
	}
	if opts.Addr != "[::1]:6380" || opts.DB != 3 {
		t.Fatalf("unexpected options: %+v", opts)
	}
}

func TestParseRedisURLAddsDefaultPortForIPv6(t *testing.T) {
	opts, err := ParseRedisURL("redis://[::1]/0")
	if err != nil {
		t.Fatalf("ParseRedisURL() error = %v", err)
	}
	if opts.Addr != "[::1]:6379" {
		t.Fatalf("Addr = %q", opts.Addr)
	}
}

func TestParseRedisURLEnablesTLSForRediss(t *testing.T) {
	opts, err := ParseRedisURL("rediss://user:pass@cache.example.com/0")
	if err != nil {
		t.Fatalf("ParseRedisURL() error = %v", err)
	}
	if opts.Addr != "cache.example.com:6379" || opts.Username != "user" || opts.Password != "pass" {
		t.Fatalf("unexpected rediss options: %+v", opts)
	}
	if opts.TLS == nil {
		t.Fatal("TLS = nil, want TLS config")
	}
	if opts.TLS.ServerName != "cache.example.com" {
		t.Fatalf("TLS.ServerName = %q", opts.TLS.ServerName)
	}
}

func TestParseRedisURLRejectsInvalidDB(t *testing.T) {
	if _, err := ParseRedisURL("redis://127.0.0.1/not-a-db"); err == nil {
		t.Fatal("ParseRedisURL() error = nil, want invalid db error")
	}
}

func TestRedisOptionsDefaultTimeouts(t *testing.T) {
	opts, err := ParseRedisURL("redis://127.0.0.1/0")
	if err != nil {
		t.Fatalf("ParseRedisURL() error = %v", err)
	}
	asynqOpt := opts.asynqOpt()
	if asynqOpt.DialTimeout <= 0 || asynqOpt.ReadTimeout <= 0 || asynqOpt.WriteTimeout <= 0 {
		t.Fatalf("timeouts not set: %+v", asynqOpt)
	}
}

func TestEnqueueRejectsEmptyTaskType(t *testing.T) {
	client := &Client{}
	if _, err := client.Enqueue(context.Background(), "", nil, EnqueueOptions{}); err == nil {
		t.Fatal("Enqueue() error = nil, want error")
	}
}
