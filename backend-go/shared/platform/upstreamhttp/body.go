package upstreamhttp

import (
	"errors"
	"io"
)

var (
	ErrResponseBodyLimitInvalid = errors.New("upstream response body limit is invalid")
	ErrResponseBodyTooLarge     = errors.New("upstream response body exceeds limit")
)

// ReadBounded retains a response body only when its complete framing fits in
// maxBytes. It stops after maxBytes+1 bytes, so callers that require draining
// a complete response should use ReadAndDrainBounded instead.
func ReadBounded(reader io.Reader, maxBytes int64) ([]byte, error) {
	if reader == nil || maxBytes <= 0 {
		return nil, ErrResponseBodyLimitInvalid
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, ErrResponseBodyTooLarge
	}
	return body, nil
}

// ReadBoundedPartial 保留已读取的响应内容，即使读取在响应尾部返回错误。
// 调用方必须先用协议完成证据校验 partial，再决定是否接受本次响应。
func ReadBoundedPartial(reader io.Reader, maxBytes int64) ([]byte, error) {
	if reader == nil || maxBytes <= 0 {
		return nil, ErrResponseBodyLimitInvalid
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if int64(len(body)) > maxBytes {
		return body[:maxBytes], ErrResponseBodyTooLarge
	}
	if err != nil {
		return body, err
	}
	return body, nil
}

// ReadAndDrainBounded retains at most maxBytes while always reading through
// EOF. This is for probes whose transport result depends on complete framing,
// even when the diagnostic body is larger than the retention budget.
func ReadAndDrainBounded(reader io.Reader, maxBytes int64) ([]byte, error) {
	if reader == nil || maxBytes <= 0 {
		return nil, ErrResponseBodyLimitInvalid
	}
	retained, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if _, err := Drain(reader); err != nil {
		return nil, err
	}
	if int64(len(retained)) > maxBytes {
		retained = retained[:maxBytes]
	}
	return retained, nil
}

// ReadAndDrainBoundedPartial 保留已读取内容并继续消费响应体到 EOF。
func ReadAndDrainBoundedPartial(reader io.Reader, maxBytes int64) ([]byte, error) {
	if reader == nil || maxBytes <= 0 {
		return nil, ErrResponseBodyLimitInvalid
	}
	retained, readErr := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if int64(len(retained)) > maxBytes {
		retained = retained[:maxBytes]
		if readErr == nil {
			readErr = ErrResponseBodyTooLarge
		}
	}
	if _, drainErr := Drain(reader); readErr == nil {
		readErr = drainErr
	}
	return retained, readErr
}

// Drain consumes a response body to EOF and returns the number of bytes read.
// The caller's request context/deadline remains responsible for stopping an
// upstream that never completes.
func Drain(reader io.Reader) (int64, error) {
	if reader == nil {
		return 0, ErrResponseBodyLimitInvalid
	}
	return io.Copy(io.Discard, reader)
}
