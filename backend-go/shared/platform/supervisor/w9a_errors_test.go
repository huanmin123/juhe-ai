package supervisor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestW9ARunWithOptionsRejectsEmptyComponents 覆盖空组件列表的快速失败。
func TestW9ARunWithOptionsRejectsEmptyComponents(t *testing.T) {
	err := RunWithOptions(context.Background(), nil, slog.Default(), Options{})
	if err == nil || !strings.Contains(err.Error(), "at least one component") {
		t.Fatalf("空组件列表必须报错: %v", err)
	}
}

// TestW9ARunLogsCloseFailuresAndRecoversClosePanic 覆盖 Close 错误日志与
// Close panic 恢复。
func TestW9ARunLogsCloseFailuresAndRecoversClosePanic(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buffer, nil))

	ctx, cancel := context.WithCancel(context.Background())
	components := []Component{
		{Name: "close-fails", Run: func(context.Context) error { return ctx.Err() }, Close: func() error {
			return errors.New("close exploded")
		}},
		{Name: "close-panics", Run: func(context.Context) error { return ctx.Err() }, Close: func() error {
			panic("close panicked")
		}},
		{Name: "close-nil", Run: func(context.Context) error { return ctx.Err() }, Close: nil},
	}
	done := make(chan error, 1)
	go func() { done <- RunWithOptions(ctx, components, logger, Options{}) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("取消后 Run 必须返回 nil: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未在取消后退出")
	}
	logged := buffer.String()
	if !strings.Contains(logged, "close exploded") {
		t.Fatal("Close 错误必须记日志")
	}
	if !strings.Contains(logged, "component close panic") || !strings.Contains(logged, "close panicked") {
		t.Fatal("Close panic 必须恢复并记日志")
	}
}

// TestW9ARunComponentRetryAndCancelPaths 覆盖组件重启循环的取消与重试日志。
func TestW9ARunComponentRetryAndCancelPaths(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buffer, nil))

	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	components := []Component{
		{Name: "flaky", Run: func(context.Context) error {
			attempts++
			return errors.New("boom")
		}},
	}
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()
	err := RunWithOptions(ctx, components, logger, Options{InitialRetryDelay: time.Millisecond, MaxRetryDelay: 2 * time.Millisecond})
	if err != nil {
		t.Fatalf("取消后必须干净退出: %v", err)
	}
	logged := buffer.String()
	if !strings.Contains(logged, "retrying") {
		t.Fatal("失败重试必须记日志")
	}
	if !strings.Contains(logged, "component stopped") {
		t.Fatal("取消后必须记 stopped 日志")
	}
}

// TestW9ARunWithAlreadyCancelledContext 覆盖启动前 ctx 已取消的分支。
func TestW9ARunWithAlreadyCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buffer, nil))
	err := RunWithOptions(ctx, []Component{{Name: "never", Run: func(context.Context) error { return nil }}}, logger, Options{})
	if err != nil {
		t.Fatalf("已取消 ctx 必须干净退出: %v", err)
	}
	if !strings.Contains(buffer.String(), "component stopped") {
		t.Fatal("已取消 ctx 必须记 stopped 日志")
	}
}

// TestW9AHelperFunctions 直测 retryDelay/normalizeOptions/loggerOrDefault/min。
func TestW9AHelperFunctions(t *testing.T) {
	options := normalizeOptions(Options{})
	if options.InitialRetryDelay != defaultInitialRetryDelay || options.MaxRetryDelay != defaultMaxRetryDelay {
		t.Fatalf("默认选项 = %+v", options)
	}
	options = normalizeOptions(Options{InitialRetryDelay: time.Second, MaxRetryDelay: time.Millisecond})
	if options.MaxRetryDelay != time.Second {
		t.Fatalf("Max < Initial 必须抬升: %+v", options)
	}

	if got := retryDelay(options, 1); got != time.Second {
		t.Fatalf("首次重试延迟 = %v", got)
	}
	// 连续失败翻倍直到封顶。
	capped := retryDelay(Options{InitialRetryDelay: time.Second, MaxRetryDelay: 4 * time.Second}, 5)
	if capped != 4*time.Second {
		t.Fatalf("重试封顶 = %v", capped)
	}
	// delay 超过 Max/2 直接返回 Max。
	if got := retryDelay(Options{InitialRetryDelay: 3 * time.Second, MaxRetryDelay: 4 * time.Second}, 3); got != 4*time.Second {
		t.Fatalf("超半直接封顶 = %v", got)
	}
	if min(1*time.Second, 2*time.Second) != 1*time.Second || min(3*time.Second, 2*time.Second) != 2*time.Second {
		t.Fatal("min 语义错误")
	}
	if loggerOrDefault(nil) == nil {
		t.Fatal("nil logger 必须回退默认")
	}
	named := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	if loggerOrDefault(named) != named {
		t.Fatal("非 nil logger 必须原样返回")
	}
}
