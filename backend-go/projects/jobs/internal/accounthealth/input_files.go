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

// LoadSignedInputFiles 是 J1 的显式后备输入源（INPUT_SOURCE=files）：只读
// Node 发布方遗留的已签名不可变文件。自 2026-09 起 sqlite/PG store 缺省走
// 各自的直读 reader，本通道仅在显式配置时启用；坏文件仍是可见的单文件错误，
// 不静默回退到任何业务库读取。
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
