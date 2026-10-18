package gatewaydispatch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
)

type transformCloseRecordingBody struct {
	io.Reader
	closed int
}

func (body *transformCloseRecordingBody) Close() error {
	body.closed++
	return nil
}

type failingResponseTransformer struct{}

func (failingResponseTransformer) TransformUpstreamResponseForAccount(UpstreamResponseTransformInput) (*GatewayUpstreamResponse, error) {
	return nil, errors.New("转换失败")
}

// BUG-0328 回归：转换器报错时响应体由引擎兜底关闭——attempt 循环错误分支
// 只拿得到 err，无人关闭就会丢弃仍持有全局并发槽的响应体。
func TestTransformUpstreamResponseForAccountErrorClosesBody(t *testing.T) {
	body := &transformCloseRecordingBody{Reader: strings.NewReader("payload")}
	response := gatewayupstream.NewGatewayUpstreamResponseForTransform(http.StatusOK, http.Header{}, body)
	engine := &Engine{ResponseTransformer: failingResponseTransformer{}}
	if _, err := engine.transformUpstreamResponseForAccount(context.Background(), AttemptInput{}, nil, nil, response); err == nil {
		t.Fatal("转换器错误应向上传播")
	}
	if body.closed != 1 {
		t.Fatalf("转换失败后响应体未被关闭: closed=%d", body.closed)
	}
}
