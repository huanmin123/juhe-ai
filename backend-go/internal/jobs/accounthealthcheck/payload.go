package accounthealthcheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	TaskType       = "account-health-check:probe"
	QueueName      = "account-health-check"
	PayloadVersion = 1
)

var ErrInvalidPayload = errors.New("invalid account health check payload")

type Task struct {
	AccountID      string `json:"accountId"`
	ConfigRevision int    `json:"configRevision"`
	UniqueKey      string `json:"uniqueKey"`
}

func Encode(task Task) ([]byte, error) {
	if err := validate(task); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Version int `json:"version"`
		Task
	}{PayloadVersion, task})
}

func Decode(data []byte) (Task, error) {
	var payload struct {
		Version int `json:"version"`
		Task
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return Task{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	if payload.Version != PayloadVersion {
		return Task{}, fmt.Errorf("%w: unsupported version %d", ErrInvalidPayload, payload.Version)
	}
	if err := validate(payload.Task); err != nil {
		return Task{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	return payload.Task, nil
}

func UniqueKey(accountID string, revision int) string {
	return fmt.Sprintf("account-health-check:%s:%d", strings.TrimSpace(accountID), revision)
}

func validate(task Task) error {
	if strings.TrimSpace(task.AccountID) == "" {
		return fmt.Errorf("account id is required")
	}
	if task.ConfigRevision < 1 {
		return fmt.Errorf("config revision must be positive")
	}
	if strings.TrimSpace(task.UniqueKey) == "" {
		return fmt.Errorf("unique key is required")
	}
	if task.UniqueKey != UniqueKey(task.AccountID, task.ConfigRevision) {
		return fmt.Errorf("unique key does not match account and config revision")
	}
	return nil
}
