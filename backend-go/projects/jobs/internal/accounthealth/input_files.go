package accounthealth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const inputFileSuffix = ".account-health-input.json"

// LoadSignedInputFiles reads only Node-published immutable files. It never
// opens the Node business SQLite database and treats a bad file as a visible
// per-file error rather than silently falling back.
func LoadSignedInputFiles(directory string, keys map[string][]byte) ([]Input, error) {
	root := strings.TrimSpace(directory)
	if root == "" {
		return nil, errors.New("account-health input 目录缺失")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("读取 account-health input 目录失败: %w", err)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), inputFileSuffix) {
			paths = append(paths, filepath.Join(root, entry.Name()))
		}
	}
	sort.Strings(paths)
	inputs := make([]Input, 0, len(paths))
	accountVersions := make(map[string]int64, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取 account-health input %q 失败: %w", filepath.Base(path), err)
		}
		input, err := VerifySignedInput(raw, keys)
		if err != nil {
			return nil, fmt.Errorf("验证 account-health input %q 失败: %w", filepath.Base(path), err)
		}
		if previous, exists := accountVersions[input.AccountID]; exists && previous >= input.InputVersion {
			return nil, fmt.Errorf("account-health input 存在重复或倒退版本：account=%s version=%d", input.AccountID, input.InputVersion)
		}
		accountVersions[input.AccountID] = input.InputVersion
		inputs = append(inputs, input)
	}
	return inputs, nil
}
