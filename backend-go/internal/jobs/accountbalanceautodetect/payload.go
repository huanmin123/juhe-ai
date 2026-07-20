package accountbalanceautodetect

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	TaskType       = "account-balance-auto-detect:probe"
	QueueName      = "account-balance-auto-detect"
	PayloadVersion = 1
)

var ErrInvalidPayload = errors.New("invalid account balance auto detect payload")

type Task struct {
	AccountID      string `json:"accountId"`
	ConfigRevision int    `json:"configRevision"`
}

func Encode(task Task) ([]byte, error) {
	task.AccountID = strings.TrimSpace(task.AccountID)
	if err := validateTask(task); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Version int `json:"version"`
		Task
	}{Version: PayloadVersion, Task: task})
}

func Decode(data []byte) (Task, error) {
	var payload struct {
		Version int `json:"version"`
		Task
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return Task{}, fmt.Errorf("%w: decode payload: %v", ErrInvalidPayload, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Task{}, fmt.Errorf("%w: trailing payload data", ErrInvalidPayload)
	}
	if payload.Version != PayloadVersion {
		return Task{}, fmt.Errorf("%w: unsupported version %d", ErrInvalidPayload, payload.Version)
	}
	payload.AccountID = strings.TrimSpace(payload.AccountID)
	if err := validateTask(payload.Task); err != nil {
		return Task{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	return payload.Task, nil
}

func validateTask(task Task) error {
	if task.AccountID == "" {
		return fmt.Errorf("account id is required")
	}
	if task.ConfigRevision < 1 {
		return fmt.Errorf("config revision must be positive")
	}
	return nil
}
