package queue

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
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

func TestNewAsynqTaskPreservesPayloadAndHeaders(t *testing.T) {
	payload := []byte(`{"version":3}`)
	headers := map[string]string{"strategy": "30"}
	task := newAsynqTask("probe", payload, headers)
	if task.Type() != "probe" || !bytes.Equal(task.Payload(), payload) || !reflect.DeepEqual(task.Headers(), headers) {
		t.Fatalf("task type=%q payload=%q headers=%v", task.Type(), task.Payload(), task.Headers())
	}
	headers["strategy"] = "changed"
	if task.Headers()["strategy"] != "30" {
		t.Fatal("Asynq task headers must not alias enqueue options")
	}
}

func TestUniqueDeduplicatesAcrossClientsWhenOnlyHeadersChange(t *testing.T) {
	server := miniredis.RunT(t)
	opts := RedisOptions{Addr: server.Addr()}
	ctx := context.Background()
	noRetry := 0
	payload := []byte(`{"version":3,"fence":{"accountId":"acct_1"}}`)

	firstClient := NewClient(opts)
	first, err := firstClient.Enqueue(ctx, "probe", payload, EnqueueOptions{
		Queue: "account-probes", MaxRetry: &noRetry, UniqueTTL: time.Minute,
		Headers: map[string]string{"strategy": "30"},
	})
	if err != nil {
		t.Fatalf("first enqueue error = %v", err)
	}
	if err := firstClient.Close(); err != nil {
		t.Fatalf("close first client: %v", err)
	}

	secondClient := NewClient(opts)
	defer func() { _ = secondClient.Close() }()
	_, err = secondClient.Enqueue(ctx, "probe", payload, EnqueueOptions{
		Queue: "account-probes", MaxRetry: &noRetry, UniqueTTL: time.Minute,
		Headers: map[string]string{"strategy": "45"},
	})
	if !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("second enqueue error = %v, want task conflict", err)
	}

	inspector := NewInspector(opts)
	defer func() { _ = inspector.Close() }()
	if err := inspector.DeleteTask(first.Queue, first.ID); err != nil {
		t.Fatalf("delete task: %v", err)
	}
}
