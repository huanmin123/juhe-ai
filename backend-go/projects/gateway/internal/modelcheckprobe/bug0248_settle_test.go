// BUG-0248 契约 4：Execute 的 settle defer 必须按返回时的变量值判 nil——
// 成功路径以 true 结算恰好一次，不再被旧 defer 以 false 二次结算；失败路径
// （非 200 / 响应读取失败）以 false 结算恰好一次。
package modelcheckprobe

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func bug0248Response(status int, body io.Reader) *http.Response {
	if body == nil {
		body = strings.NewReader(`{"model":"gpt-test","choices":[{"message":{"content":"OK-MODEL-CHECK"}}]}`)
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(body),
	}
}

func TestBug0248SettleCalledExactlyOnce(t *testing.T) {
	run := func(response *http.Response) (Result, *wlDispatcherPlain) {
		dispatcher := &wlDispatcherPlain{response: response}
		result, err := Execute(context.Background(), mustWlBasicRequest(t), Options{
			Endpoint: "https://upstream.example", Dispatcher: dispatcher, Timeout: time.Second,
		})
		if err != nil {
			t.Fatalf("Execute err=%v", err)
		}
		return result, dispatcher
	}
	t.Run("成功路径结算一次且为 true", func(t *testing.T) {
		result, dispatcher := run(bug0248Response(http.StatusOK, nil))
		if !result.Success {
			t.Fatalf("result=%+v", result)
		}
		if len(dispatcher.settleCalls) != 1 || dispatcher.settleCalls[0] != true {
			t.Fatalf("成功路径 settle 调用序列=%v want [true]", dispatcher.settleCalls)
		}
	})
	t.Run("非 200 状态结算一次 false", func(t *testing.T) {
		result, dispatcher := run(bug0248Response(http.StatusBadGateway, nil))
		if result.Success {
			t.Fatalf("result=%+v", result)
		}
		if len(dispatcher.settleCalls) != 1 || dispatcher.settleCalls[0] != false {
			t.Fatalf("非 200 路径 settle 调用序列=%v want [false]", dispatcher.settleCalls)
		}
	})
	t.Run("响应读取失败结算一次 false", func(t *testing.T) {
		result, dispatcher := run(bug0248Response(http.StatusOK, wlErrReader{}))
		if result.Success {
			t.Fatalf("result=%+v", result)
		}
		if len(dispatcher.settleCalls) != 1 || dispatcher.settleCalls[0] != false {
			t.Fatalf("读取失败路径 settle 调用序列=%v want [false]", dispatcher.settleCalls)
		}
	})
}
