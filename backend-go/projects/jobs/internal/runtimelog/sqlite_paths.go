package runtimelog

// SQLite 路径比较/包含判定 helper：原随一次性命令 --migrate-runtime-log-legacy-sqlite
// 定义，该命令已删除，但 Config 校验（同源/包含检查）仍在用，故独立保留。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func sameSQLitePath(left, right string) (bool, error) {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false, fmt.Errorf("SQLite 路径不能为空: left=%q right=%q", left, right)
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	if leftErr != nil && !errors.Is(leftErr, os.ErrNotExist) {
		return false, fmt.Errorf("stat 左侧 SQLite 路径 %q 失败: %w", left, leftErr)
	}
	if rightErr != nil && !errors.Is(rightErr, os.ErrNotExist) {
		return false, fmt.Errorf("stat 右侧 SQLite 路径 %q 失败: %w", right, rightErr)
	}
	if leftErr == nil && rightErr == nil {
		return os.SameFile(leftInfo, rightInfo), nil
	}
	leftAbs, err := canonicalSQLitePath(left)
	if err != nil {
		return false, fmt.Errorf("解析左侧 SQLite 路径 %q 失败: %w", left, err)
	}
	rightAbs, err := canonicalSQLitePath(right)
	if err != nil {
		return false, fmt.Errorf("解析右侧 SQLite 路径 %q 失败: %w", right, err)
	}
	if equalSQLitePath(leftAbs, rightAbs) {
		return true, nil
	}
	return false, nil
}

func sqlitePathWithin(root, candidate string) (bool, error) {
	rootPath, err := canonicalSQLitePath(root)
	if err != nil {
		return false, fmt.Errorf("解析 SQLite 根路径 %q 失败: %w", root, err)
	}
	candidatePath, err := canonicalSQLitePath(candidate)
	if err != nil {
		return false, fmt.Errorf("解析 SQLite 候选路径 %q 失败: %w", candidate, err)
	}
	relative, err := filepath.Rel(rootPath, candidatePath)
	if err != nil {
		return false, fmt.Errorf("比较 SQLite 路径 %q 与根路径 %q 失败: %w", candidate, root, err)
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))), nil
}

func canonicalSQLitePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("SQLite 路径不能为空")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("获取绝对路径失败: %w", err)
	}
	abs = filepath.Clean(abs)
	info, err := os.Lstat(abs)
	if err == nil {
		resolved, evalErr := filepath.EvalSymlinks(abs)
		if evalErr != nil {
			return "", fmt.Errorf("解析符号链接失败: %w", evalErr)
		}
		if _, statErr := os.Stat(abs); statErr != nil {
			return "", fmt.Errorf("stat 路径目标失败: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if _, statErr := os.Stat(resolved); statErr != nil {
				return "", fmt.Errorf("stat 符号链接目标失败: %w", statErr)
			}
		}
		return filepath.Clean(resolved), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("检查路径失败: %w", err)
	}
	// On Windows, Lstat can report ERROR_FILE_NOT_FOUND for a dangling
	// symlink instead of returning the link metadata. Inspect the parent
	// directory entry in that case; fail closed rather than treating it as a
	// normal not-yet-created SQLite file.
	if danglingSQLiteSymlink(abs) {
		return "", fmt.Errorf("SQLite 路径是悬空符号链接")
	}
	parent := filepath.Dir(abs)
	suffix := []string{filepath.Base(abs)}
	for {
		_, parentErr := os.Lstat(parent)
		if parentErr == nil {
			resolvedParent, evalErr := filepath.EvalSymlinks(parent)
			if evalErr != nil {
				return "", fmt.Errorf("解析真实父目录 %q 失败: %w", parent, evalErr)
			}
			parentStat, statErr := os.Stat(parent)
			if statErr != nil {
				return "", fmt.Errorf("stat 真实父目录 %q 失败: %w", parent, statErr)
			}
			if !parentStat.IsDir() {
				return "", fmt.Errorf("真实父路径 %q 不是目录", parent)
			}
			return filepath.Clean(filepath.Join(append([]string{resolvedParent}, suffix...)...)), nil
		}
		if !errors.Is(parentErr, os.ErrNotExist) {
			return "", fmt.Errorf("检查父路径 %q 失败: %w", parent, parentErr)
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", fmt.Errorf("路径没有可解析的真实父目录")
		}
		suffix = append([]string{filepath.Base(parent)}, suffix...)
		parent = next
	}
}

func danglingSQLiteSymlink(path string) bool {
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return false
	}
	base := filepath.Base(path)
	for _, entry := range entries {
		if entry.Name() != base {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return true
		}
		info, infoErr := entry.Info()
		return infoErr == nil && info.Mode()&os.ModeSymlink != 0
	}
	return false
}

func equalSQLitePath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}
