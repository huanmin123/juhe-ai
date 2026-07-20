package accounttest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	TaskType       = "account-test:run"
	QueueName      = "account-test"
	PayloadVersion = 1
)

var ErrInvalidPayload = errors.New("invalid account test task payload")

type EnqueuePayload struct {
	Version int    `json:"version"`
	TaskID  string `json:"taskId"`
}

func Encode(payload EnqueuePayload) ([]byte, error) {
	payload.TaskID = strings.TrimSpace(payload.TaskID)
	if payload.Version == 0 {
		payload.Version = PayloadVersion
	}
	if payload.Version != PayloadVersion {
		return nil, fmt.Errorf("%w: unsupported payload version: %d", ErrInvalidPayload, payload.Version)
	}
	if payload.TaskID == "" {
		return nil, fmt.Errorf("%w: task id is required", ErrInvalidPayload)
	}
	return json.Marshal(payload)
}

func Decode(data []byte) (EnqueuePayload, error) {
	var payload EnqueuePayload
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&payload); err != nil {
		return EnqueuePayload{}, fmt.Errorf("%w: decode payload: %v", ErrInvalidPayload, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return EnqueuePayload{}, fmt.Errorf("%w: trailing payload data", ErrInvalidPayload)
	}
	payload.TaskID = strings.TrimSpace(payload.TaskID)
	if payload.Version != PayloadVersion {
		return EnqueuePayload{}, fmt.Errorf("%w: unsupported payload version: %d", ErrInvalidPayload, payload.Version)
	}
	if payload.TaskID == "" {
		return EnqueuePayload{}, fmt.Errorf("%w: task id is required", ErrInvalidPayload)
	}
	return payload, nil
}
