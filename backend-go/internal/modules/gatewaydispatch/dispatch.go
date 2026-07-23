// Package gatewaydispatch provides the narrow HTTP seam between the Go
// gateway's already-classified attempt and transport-specific response work.
// Candidate selection, retry policy, proxy/SSRF policy, and protocol inspection
// remain injected responsibilities.
package gatewaydispatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
	"juhe-ai/backend-go/internal/modules/gatewayupstream"
	"juhe-ai/backend-go/internal/protocols/gateway"
)

const (
	DefaultMaxResponseBodyBytes int64 = 16 << 20
	MaxResponseBodyBytes        int64 = 64 << 20
)

var (
	ErrClientRequired       = errors.New("网关 dispatch 缺少 HTTP client")
	ErrResponseMissing      = errors.New("上游响应为空")
	ErrResponseBodyMissing  = errors.New("上游响应缺少 body")
	ErrResponseBodyTooLarge = errors.New("上游响应体超过限制")
	ErrResponseBodyRead     = errors.New("读取上游响应体失败")
	ErrResponseBodyClose    = errors.New("关闭上游响应体失败")
)

// Doer is deliberately smaller than *http.Client so tests and future proxy
// transports can be injected without making dispatch own transport policy.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

type Dispatcher struct {
	Client               Doer
	Builder              gatewayupstream.Builder
	MaxResponseBodyBytes int64
}

type Result struct {
	Request    *http.Request
	Response   *http.Response
	Definition gateway.Definition
}

// Dispatch builds one already-authorized attempt and sends it through the
// injected client. It does not classify HTTP status codes or decide retries.
func (d Dispatcher) Dispatch(input gatewayupstream.Input) (Result, error) {
	if d.Client == nil {
		return Result{}, ErrClientRequired
	}
	request, definition, err := d.Builder.Build(input)
	if err != nil {
		return Result{}, err
	}
	response, err := d.Client.Do(request)
	if err != nil {
		return Result{}, fmt.Errorf("gateway upstream transport: %w", err)
	}
	if response == nil {
		return Result{}, ErrResponseMissing
	}
	if response.Body == nil {
		return Result{Request: request, Response: response, Definition: definition}, ErrResponseBodyMissing
	}
	return Result{Request: request, Response: response, Definition: definition}, nil
}

// ReadBody reads and closes a non-stream response with an explicit bound.
// Callers must not use Response.Body after this method returns.
func (d Dispatcher) ReadBody(result Result) ([]byte, error) {
	if result.Response == nil {
		return nil, ErrResponseMissing
	}
	if result.Response.Body == nil {
		return nil, ErrResponseBodyMissing
	}
	limit := d.MaxResponseBodyBytes
	if limit <= 0 {
		limit = DefaultMaxResponseBodyBytes
	}
	if limit > MaxResponseBodyBytes {
		limit = MaxResponseBodyBytes
	}
	body := result.Response.Body
	raw, readErr := io.ReadAll(io.LimitReader(body, limit+1))
	closeErr := body.Close()
	if int64(len(raw)) > limit {
		if closeErr != nil {
			return nil, errors.Join(ErrResponseBodyTooLarge, closeFailure(closeErr))
		}
		return nil, fmt.Errorf("%w: limit=%d", ErrResponseBodyTooLarge, limit)
	}
	if readErr != nil {
		if closeErr != nil {
			return nil, errors.Join(fmt.Errorf("%w: %w", ErrResponseBodyRead, readErr), closeFailure(closeErr))
		}
		return nil, fmt.Errorf("%w: %w", ErrResponseBodyRead, readErr)
	}
	if closeErr != nil {
		return nil, closeFailure(closeErr)
	}
	return raw, nil
}

// Relay moves a dispatched streaming response through the bounded relay. The
// response body is always closed before returning; a close error is joined with
// a relay error rather than silently discarded.
func (d Dispatcher) Relay(
	ctx context.Context,
	result Result,
	sink gatewaystreamrelay.Sink,
	options gatewaystreamrelay.Options,
) (gatewaystreamrelay.Result, error) {
	if result.Response == nil {
		return gatewaystreamrelay.Result{}, ErrResponseMissing
	}
	if result.Response.Body == nil {
		return gatewaystreamrelay.Result{}, ErrResponseBodyMissing
	}
	if options.StatusCode == 0 {
		options.StatusCode = result.Response.StatusCode
	}
	relayResult, relayErr := gatewaystreamrelay.Relay(ctx, responseBodySource{body: result.Response.Body}, sink, options)
	closeErr := result.Response.Body.Close()
	if relayErr != nil && closeErr != nil {
		return relayResult, errors.Join(relayErr, closeFailure(closeErr))
	}
	if relayErr != nil {
		return relayResult, relayErr
	}
	if closeErr != nil {
		return relayResult, closeFailure(closeErr)
	}
	return relayResult, nil
}

type responseBodySource struct {
	body io.Reader
}

func closeFailure(err error) error {
	return fmt.Errorf("%w: %w", ErrResponseBodyClose, err)
}

func (s responseBodySource) Read(ctx context.Context, p []byte) (int, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
	return s.body.Read(p)
}
