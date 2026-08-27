package accounthealth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const requestFileSuffix = ".account-health-request.json"

// LoadSignedProbeRequests accepts only explicit Node-published request facts.
// A malformed file is a visible fault, never an implicit fallback to Node IPC
// or a best-effort execution path.
func LoadSignedProbeRequests(directory string, keys map[string][]byte) ([]ProbeRequest, error) {
	root := strings.TrimSpace(directory)
	if root == "" {
		return nil, errors.New("account-health request 目录缺失")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("读取 account-health request 目录失败: %w", err)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), requestFileSuffix) {
			paths = append(paths, filepath.Join(root, entry.Name()))
		}
	}
	sort.Strings(paths)
	requests := make([]ProbeRequest, 0, len(paths))
	seen := map[string]struct{}{}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取 account-health request %q 失败: %w", filepath.Base(path), err)
		}
		payload, err := VerifySignedPayload(raw, keys)
		if err != nil {
			return nil, fmt.Errorf("验证 account-health request %q 失败: %w", filepath.Base(path), err)
		}
		var request ProbeRequest
		if err := json.Unmarshal(payload, &request); err != nil {
			return nil, fmt.Errorf("解析 account-health request %q 失败: %w", filepath.Base(path), err)
		}
		if err := validateProbeRequest(request); err != nil {
			return nil, fmt.Errorf("account-health request %q 无效: %w", filepath.Base(path), err)
		}
		if _, exists := seen[request.RequestID]; exists {
			return nil, fmt.Errorf("account-health request 重复 request ID: %s", request.RequestID)
		}
		seen[request.RequestID] = struct{}{}
		request.sourcePath = path
		requests = append(requests, request)
	}
	return requests, nil
}

func validateProbeRequest(request ProbeRequest) error {
	if strings.TrimSpace(request.RequestID) == "" || strings.TrimSpace(request.AccountID) == "" || strings.TrimSpace(request.Reason) == "" {
		return errors.New("request 缺少 ID、account 或 reason")
	}
	if request.InputVersion < 1 || request.ConfigRevision < 1 || request.DispatchRevision < 1 || request.Deadline.IsZero() {
		return errors.New("request fence 或 deadline 无效")
	}
	if request.SourceFence != nil {
		fence := request.SourceFence
		if strings.TrimSpace(fence.StateKey) == "" || strings.TrimSpace(fence.SourceFenceID) == "" || strings.TrimSpace(fence.RuntimeKey) == "" || fence.AccountID != request.AccountID || fence.ConfigRevision != request.ConfigRevision || fence.SourceGeneration < 1 || fence.ProbeGeneration < 1 {
			return errors.New("request source fence 无效")
		}
	}
	if request.KeyModelFence != nil {
		fence := request.KeyModelFence
		if !isCapabilityHash(fence.CapabilityHash) || strings.TrimSpace(fence.KeyFingerprint) == "" || strings.TrimSpace(fence.OwnerID) == "" || fence.DispatchRevision != request.DispatchRevision {
			return errors.New("request key-model fence 无效")
		}
	}
	return nil
}

func isCapabilityHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func requestDeadlineExpired(request ProbeRequest, now time.Time) bool {
	return !request.Deadline.After(now)
}
