package gatewaybody

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseJSONValueValidDocuments(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want any
	}{
		{name: "object", raw: `{"a":1,"b":[true,null]}`, want: map[string]any{"a": float64(1), "b": []any{true, nil}}},
		{name: "top level array", raw: `[1,2]`, want: []any{float64(1), float64(2)}},
		{name: "top level string", raw: `"text"`, want: "text"},
		{name: "top level number", raw: `3.5`, want: 3.5},
		{name: "top level true", raw: `true`, want: true},
		{name: "top level null", raw: `null`, want: nil},
		{name: "whitespace padded", raw: "  \n\t{\n\"a\": 1\n}  ", want: map[string]any{"a": float64(1)}},
		{name: "duplicate keys keep last", raw: `{"a":1,"a":2}`, want: map[string]any{"a": float64(2)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseJSONValue([]byte(tt.raw))
			if err != nil {
				t.Fatalf("ParseJSONValue() error = %v", err)
			}
			if !deepEqualAny(got, tt.want) {
				t.Fatalf("ParseJSONValue() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseJSONValueNumberParity(t *testing.T) {
	t.Run("overflow becomes +Inf like V8", func(t *testing.T) {
		got, err := ParseJSONValue([]byte(`{"a":1e400}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		object := got.(map[string]any)
		value := object["a"].(float64)
		if !math.IsInf(value, 1) {
			t.Fatalf("a = %v, want +Inf", value)
		}
	})
	t.Run("negative overflow becomes -Inf", func(t *testing.T) {
		got, err := ParseJSONValue([]byte(`[-1e400]`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		value := got.([]any)[0].(float64)
		if !math.IsInf(value, -1) {
			t.Fatalf("value = %v, want -Inf", value)
		}
	})
	t.Run("underflow becomes zero", func(t *testing.T) {
		got, err := ParseJSONValue([]byte(`1e-400`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.(float64) != 0 {
			t.Fatalf("value = %v, want 0", got)
		}
	})
	t.Run("large integers stay float64", func(t *testing.T) {
		got, err := ParseJSONValue([]byte(`{"n":9007199254740993}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.(map[string]any)["n"].(float64) != 9007199254740992 {
			t.Fatalf("float64 rounding parity broken: %v", got)
		}
	})
}

func TestParseJSONValueInvalidDocuments(t *testing.T) {
	tests := []string{
		``,
		`   `,
		`{`,
		`{"a":}`,
		`{"a":1} trailing`,
		`[1,2]]`,
		`{"a":"\x"}`,
		`hehe`,
		`{"a":01}`,
		`{'a':1}`,
		`undefined`,
		`{"a":1,}`,
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			got, err := ParseJSONValue([]byte(raw))
			if err == nil {
				t.Fatalf("expected error, got %#v", got)
			}
			if !IsInvalidJSONError(err) {
				t.Fatalf("expected InvalidJSONError, got %T", err)
			}
			if err.Error() != "网关 JSON 请求体必须是有效 JSON" {
				t.Fatalf("error copy mismatch: %q", err.Error())
			}
		})
	}
}

func TestJSONWorkerErrorCopies(t *testing.T) {
	if got := (&JSONWorkerTimeoutError{TimeoutMS: 250}).Error(); got != "网关 JSON worker 250ms 超时" {
		t.Fatalf("timeout copy = %q", got)
	}
	if got := (&JSONWorkerMaterializationTimeoutError{TimeoutMS: 500}).Error(); got != "网关 JSON worker 任务超时（500ms）" {
		t.Fatalf("materialization timeout copy = %q", got)
	}
	if ErrQueueFull.Error() != "网关 JSON worker 队列已满，请稍后重试" {
		t.Fatalf("queue full copy = %q", ErrQueueFull.Error())
	}
	if ErrCanceled.Error() != "网关 JSON worker 任务已取消" {
		t.Fatalf("canceled copy = %q", ErrCanceled.Error())
	}
	if ErrStopped.Error() != "网关 JSON worker 已关闭" {
		t.Fatalf("stopped copy = %q", ErrStopped.Error())
	}
}

func TestJSONParserParseAndScan(t *testing.T) {
	parser := NewJSONParser(JSONParserOptions{})
	defer parser.Stop()

	t.Run("parse on pool", func(t *testing.T) {
		value, err := parser.ParseJSONBody(context.Background(), []byte(`{"model":"gpt-4o"}`), time.Second)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if object := value.(map[string]any); object["model"] != "gpt-4o" {
			t.Fatalf("model = %v", object["model"])
		}
	})

	t.Run("metadata on pool", func(t *testing.T) {
		metadata, err := parser.ExtractJSONBodyMetadataAsync(context.Background(), []byte(`{"model":"gpt-4o","tools":[{"type":"image_generation"}]}`), time.Second)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if metadata.Model == nil || *metadata.Model != "gpt-4o" || !metadata.ImageGeneration {
			t.Fatalf("unexpected metadata: %+v", metadata)
		}
	})

	t.Run("invalid json classifies", func(t *testing.T) {
		_, err := parser.ParseJSONBody(context.Background(), []byte(`nope`), time.Second)
		if !IsInvalidJSONError(err) {
			t.Fatalf("expected invalid json, got %v", err)
		}
	})

	t.Run("pre-canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := parser.ParseJSONBody(ctx, []byte(`{}`), time.Second)
		if !IsCanceledError(err) {
			t.Fatalf("expected canceled, got %v", err)
		}
	})
}

func TestJSONParserQueueFullAdmission(t *testing.T) {
	parser := NewJSONParser(JSONParserOptions{PoolSize: 1, MaxQueuedJobs: 2})
	defer parser.Stop()

	release := make(chan struct{})
	var once sync.Once
	closeRelease := func() { once.Do(func() { close(release) }) }
	defer closeRelease()
	parser.scanFunc = func(raw []byte) JSONBodyMetadata {
		<-release
		return JSONBodyMetadata{}
	}

	// Job 1 occupies the only worker; jobs 2 and 3 fill the two queue slots.
	// Each step waits for a stable pool state so the final submission
	// deterministically hits the queue-full admission.
	submitBlocked := func() <-chan error {
		errCh := make(chan error, 1)
		go func() {
			_, err := parser.ExtractJSONBodyMetadataAsync(context.Background(), []byte(`{}`), 10*time.Second)
			errCh <- err
			closeRelease()
		}()
		return errCh
	}
	blocked := []<-chan error{submitBlocked()}
	waitFor(t, 10*time.Second, func() bool {
		parser.mu.Lock()
		defer parser.mu.Unlock()
		return parser.busyWorkers == 1
	})
	blocked = append(blocked, submitBlocked())
	waitFor(t, 10*time.Second, func() bool {
		parser.mu.Lock()
		defer parser.mu.Unlock()
		return len(parser.queue) == 1
	})
	blocked = append(blocked, submitBlocked())
	waitFor(t, 10*time.Second, func() bool {
		parser.mu.Lock()
		defer parser.mu.Unlock()
		return len(parser.queue) == 2
	})
	_ = blocked

	_, err := parser.ExtractJSONBodyMetadataAsync(context.Background(), []byte(`{}`), 10*time.Second)
	if !IsQueueFullError(err) {
		t.Fatalf("expected queue full, got %v", err)
	}
	if err.Error() != "网关 JSON worker 队列已满，请稍后重试" {
		t.Fatalf("queue full copy = %q", err.Error())
	}
}

func TestJSONParserTimeoutPath(t *testing.T) {
	parser := NewJSONParser(JSONParserOptions{PoolSize: 1})
	defer parser.Stop()

	release := make(chan struct{})
	parser.scanFunc = func(raw []byte) JSONBodyMetadata {
		<-release
		return JSONBodyMetadata{}
	}
	defer close(release)

	start := time.Now()
	_, err := parser.ExtractJSONBodyMetadataAsync(context.Background(), []byte(`{}`), 50*time.Millisecond)
	if !IsTimeoutError(err) {
		t.Fatalf("expected timeout, got %v", err)
	}
	if err.Error() != "网关 JSON worker 50ms 超时" {
		t.Fatalf("timeout copy = %q", err.Error())
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout did not fire promptly: %v", elapsed)
	}
	// Capacity must be released by the abandonment.
	waitFor(t, time.Second, func() bool {
		parser.mu.Lock()
		defer parser.mu.Unlock()
		return parser.activeBytes == 0
	})
}

func TestJSONParserCancelWhileQueued(t *testing.T) {
	parser := NewJSONParser(JSONParserOptions{PoolSize: 1})
	defer parser.Stop()

	release := make(chan struct{})
	parser.scanFunc = func(raw []byte) JSONBodyMetadata {
		<-release
		return JSONBodyMetadata{}
	}
	defer close(release)

	// Occupy the worker so the cancellable job really waits in the queue.
	blocked := make(chan error, 1)
	go func() {
		_, err := parser.ExtractJSONBodyMetadataAsync(context.Background(), []byte(`{}`), 10*time.Second)
		blocked <- err
	}()
	waitFor(t, 10*time.Second, func() bool {
		parser.mu.Lock()
		defer parser.mu.Unlock()
		return parser.busyWorkers == 1
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := parser.ExtractJSONBodyMetadataAsync(ctx, []byte(`{}`), 10*time.Second)
		errCh <- err
	}()
	waitFor(t, 10*time.Second, func() bool {
		parser.mu.Lock()
		defer parser.mu.Unlock()
		return len(parser.queue) == 1
	})
	cancel()
	select {
	case err := <-errCh:
		if !IsCanceledError(err) {
			t.Fatalf("expected canceled, got %v", err)
		}
		if err.Error() != "网关 JSON worker 任务已取消" {
			t.Fatalf("canceled copy = %q", err.Error())
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("cancel did not reject the queued job")
	}
}

func TestJSONParserStopRejectsQueuedJobs(t *testing.T) {
	parser := NewJSONParser(JSONParserOptions{PoolSize: 1})

	release := make(chan struct{})
	parser.scanFunc = func(raw []byte) JSONBodyMetadata {
		<-release
		return JSONBodyMetadata{}
	}
	defer close(release)

	blocked := make(chan error, 1)
	go func() {
		_, err := parser.ExtractJSONBodyMetadataAsync(context.Background(), []byte(`{}`), 10*time.Second)
		blocked <- err
	}()
	waitFor(t, 10*time.Second, func() bool {
		parser.mu.Lock()
		defer parser.mu.Unlock()
		return parser.busyWorkers == 1
	})
	errCh := make(chan error, 1)
	go func() {
		_, err := parser.ExtractJSONBodyMetadataAsync(context.Background(), []byte(`{}`), 10*time.Second)
		errCh <- err
	}()
	waitFor(t, 10*time.Second, func() bool {
		parser.mu.Lock()
		defer parser.mu.Unlock()
		return len(parser.queue) == 1
	})
	parser.Stop()
	select {
	case err := <-errCh:
		if !errors.Is(err, ErrStopped) {
			t.Fatalf("expected stopped, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("stop did not reject the queued job")
	}
}

func TestParseRequestJSONBodyMaterialization(t *testing.T) {
	parser := NewJSONParser(JSONParserOptions{})
	defer parser.Stop()

	t.Run("parses and updates state", func(t *testing.T) {
		req := &Request{RawBody: []byte(`{"model":"gpt-4o"}`), State: &BodyState{IsJSON: true, JSONParseStatus: JSONParseStatusScannedJSON}}
		parsed, err := parser.ParseRequestJSONBody(context.Background(), req, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if object := parsed.(map[string]any); object["model"] != "gpt-4o" {
			t.Fatalf("parsed = %#v", parsed)
		}
		if req.State.JSONParseStatus != JSONParseStatusParsed {
			t.Fatalf("status = %v", req.State.JSONParseStatus)
		}
		if req.Serialized == nil || req.Serialized.Parsed["model"] != "gpt-4o" {
			t.Fatalf("serialized binding missing: %+v", req.Serialized)
		}
		// Second call returns the cached object without reparsing.
		again, err := parser.ParseRequestJSONBody(context.Background(), req, 0)
		if err != nil || again.(map[string]any)["model"] != "gpt-4o" {
			t.Fatalf("cached call = %#v, %v", again, err)
		}
	})

	t.Run("empty raw body returns nil", func(t *testing.T) {
		req := &Request{}
		parsed, err := parser.ParseRequestJSONBody(context.Background(), req, 0)
		if parsed != nil || err != nil {
			t.Fatalf("parsed = %#v, err = %v", parsed, err)
		}
	})

	t.Run("canceled caller context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		req := &Request{RawBody: []byte(`{"a":1}`)}
		cancel()
		if _, err := parser.ParseRequestJSONBody(ctx, req, 0); !IsCanceledError(err) {
			t.Fatalf("expected canceled, got %v", err)
		}
	})

	t.Run("caller timeout surfaces the materialization copy", func(t *testing.T) {
		release := make(chan struct{})
		parser.scanFunc = func(raw []byte) JSONBodyMetadata { return JSONBodyMetadata{} }
		parser.parseFunc = func(ctx context.Context, raw []byte) (any, error) {
			<-release
			return map[string]any{}, nil
		}
		defer close(release)
		defer func() { parser.parseFunc = nil; parser.scanFunc = nil }()
		req := &Request{RawBody: []byte(`{"a":1}`)}
		_, err := parser.ParseRequestJSONBody(context.Background(), req, 60*time.Millisecond)
		if err == nil {
			t.Fatalf("expected timeout")
		}
		var timeoutErr *JSONWorkerMaterializationTimeoutError
		if !errors.As(err, &timeoutErr) {
			t.Fatalf("expected materialization timeout, got %v", err)
		}
		if err.Error() != "网关 JSON worker 任务超时（60ms）" {
			t.Fatalf("copy = %q", err.Error())
		}
		// Wait for the materialization job to be enqueued so the hook reset
		// below is ordered after the enqueue-time hook snapshot.
		waitFor(t, 2*time.Second, func() bool {
			parser.mu.Lock()
			defer parser.mu.Unlock()
			return parser.busyWorkers == 1 || len(parser.queue) == 1
		})
	})

	t.Run("failed materialization is reused for the same raw body", func(t *testing.T) {
		req := &Request{RawBody: []byte(`not-json`)}
		if _, err := parser.ParseRequestJSONBody(context.Background(), req, 0); !IsInvalidJSONError(err) {
			t.Fatalf("expected invalid json, got %v", err)
		}
		if _, err := parser.ParseRequestJSONBody(context.Background(), req, 0); !IsInvalidJSONError(err) {
			t.Fatalf("expected the cached failure, got %v", err)
		}
	})

	t.Run("raw body replacement restarts materialization", func(t *testing.T) {
		req := &Request{RawBody: []byte(`{"a":1}`)}
		first, err := parser.ParseRequestJSONBody(context.Background(), req, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		ReplaceGatewayJSONBody(req, map[string]any{"b": float64(2)})
		second, err := parser.ParseRequestJSONBody(context.Background(), req, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if deepEqualAny(first, second) || second.(map[string]any)["b"] != float64(2) {
			t.Fatalf("expected the replacement body to be parsed, got %#v", second)
		}
	})

	t.Run("request lifecycle cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		req := &Request{RawBody: []byte(`{"a":1}`), ctx: ctx}
		cancel()
		if _, err := parser.ParseRequestJSONBody(context.Background(), req, 0); !IsCanceledError(err) {
			t.Fatalf("expected canceled, got %v", err)
		}
	})
}

func TestJSONParserLargeBodyBoundary(t *testing.T) {
	parser := NewJSONParser(JSONParserOptions{})
	defer parser.Stop()

	// Construct a multi-megabyte JSON body to exercise the bounded parse on
	// large payloads without network access.
	var builder strings.Builder
	builder.WriteString(`{"model":"gpt-4o","messages":[`)
	for i := 0; i < 40000; i++ {
		if i > 0 {
			builder.WriteString(",")
		}
		fmt.Fprintf(&builder, `{"role":"user","content":"%0128d"}`, i)
	}
	builder.WriteString(`]}`)
	raw := []byte(builder.String())
	if len(raw) < 5*1024*1024 {
		t.Fatalf("constructed body too small: %d", len(raw))
	}

	value, err := parser.ParseJSONBody(context.Background(), raw, 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	object := value.(map[string]any)
	if object["model"] != "gpt-4o" {
		t.Fatalf("model = %v", object["model"])
	}
	messages := object["messages"].([]any)
	if len(messages) != 40000 {
		t.Fatalf("messages = %d", len(messages))
	}

	metadata, err := parser.ExtractJSONBodyMetadataAsync(context.Background(), raw, 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if metadata.Model == nil || *metadata.Model != "gpt-4o" {
		t.Fatalf("metadata = %+v", metadata)
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}

func deepEqualAny(a, b any) bool {
	switch typedA := a.(type) {
	case map[string]any:
		typedB, ok := b.(map[string]any)
		if !ok || len(typedA) != len(typedB) {
			return false
		}
		for key, value := range typedA {
			other, ok := typedB[key]
			if !ok || !deepEqualAny(value, other) {
				return false
			}
		}
		return true
	case []any:
		typedB, ok := b.([]any)
		if !ok || len(typedA) != len(typedB) {
			return false
		}
		for index := range typedA {
			if !deepEqualAny(typedA[index], typedB[index]) {
				return false
			}
		}
		return true
	case float64:
		valueB, ok := b.(float64)
		return ok && typedA == valueB
	case string:
		valueB, ok := b.(string)
		return ok && typedA == valueB
	case bool:
		valueB, ok := b.(bool)
		return ok && typedA == valueB
	case nil:
		return b == nil
	default:
		return false
	}
}
