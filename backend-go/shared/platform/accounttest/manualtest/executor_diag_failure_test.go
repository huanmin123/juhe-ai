package manualtest

import (
	"context"
	"errors"
	"testing"
)

func TestManualTestDiagFailureResult(t *testing.T) {
	configErr := errors.New("账户没有可用于连接测试的请求形态")
	if got := manualTestDiagFailureResult(context.Background(), configErr); got.Canceled || got.Success || got.Message != configErr.Error() {
		t.Fatalf("配置错误必须落失败结果：%+v", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := manualTestDiagFailureResult(ctx, context.Canceled); !got.Canceled {
		t.Fatalf("上下文取消必须落取消结果：%+v", got)
	}
}
