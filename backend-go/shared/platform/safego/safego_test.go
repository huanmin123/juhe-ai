package safego

import (
	"testing"
	"time"
)

func TestGoRecoversPanic(t *testing.T) {
	done := make(chan struct{})
	Go("test.boom", func() {
		defer close(done)
		panic("boom")
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("panic goroutine 必须被 recover 并继续执行到 deferred close")
	}
}

func TestRecoverStopsPanicFromEscaping(t *testing.T) {
	// 直接形态：defer safego.Recover(name) 必须真正拦住 panic（回归覆盖：
	// recover() 隔层包装时会静默失效，见包注释）。
	func() {
		defer Recover("test.barrier")
		panic("must not escape")
	}()
}

func TestRecoverIgnoresNormalReturn(t *testing.T) {
	defer Recover("test.normal")
}

func TestHandlePassesPanicValueToCallback(t *testing.T) {
	notified := make(chan any, 1)
	func() {
		defer Handle("test.value", func(recovered any) {
			notified <- recovered
		})
		panic("captured")
	}()
	select {
	case recovered := <-notified:
		if recovered != "captured" {
			t.Fatalf("recovered=%v, 期望捕获 panic 值", recovered)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("panic 未回调 onPanic")
	}
}

func TestHandleIgnoresNormalReturn(t *testing.T) {
	called := false
	defer func() {
		if called {
			t.Fatal("正常返回不得触发 onPanic")
		}
	}()
	defer Handle("test.normal", func(any) { called = true })
}

func TestHandleCallbackRunsAfterRecover(t *testing.T) {
	// onPanic 本身再 panic 不得重新炸出进程边界之外——它已处于 recover
	// 之后；此处只验证回调真实执行（语义见 Handle 文档）。
	executed := make(chan struct{})
	func() {
		defer Handle("test.order", func(any) {
			close(executed)
		})
		panic("order")
	}()
	select {
	case <-executed:
	case <-time.After(2 * time.Second):
		t.Fatal("onPanic 未执行")
	}
}
