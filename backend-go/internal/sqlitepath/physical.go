// Package sqlitepath contains physical-file checks shared by Go sidecar
// components that must remain isolated from Node-owned SQLite shards.
package sqlitepath

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var usageShardRelativePath = regexp.MustCompile(`^(\d{4})[\\/](\d{2})[\\/](\d{2})[\\/]usage-(\d{8})-s\d+\.sqlite3$`)

func RequirePhysicalRoot(root, environmentName string) error {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", environmentName, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s must not be a symbolic link", environmentName)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s must be a directory", environmentName)
	}
	return nil
}

func ListUsageShardFiles(root string) ([]string, error) {
	entries := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && path == root {
				return nil
			}
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("usage shard path must not be a symbolic link: %q", path)
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		matches := usageShardRelativePath.FindStringSubmatch(filepath.ToSlash(relative))
		if matches == nil {
			return nil
		}
		if matches[1]+matches[2]+matches[3] != matches[4] {
			return fmt.Errorf("usage shard date directory does not match filename: %q", path)
		}
		entries = append(entries, path)
		return nil
	})
	return entries, err
}

func SameFile(left, right string) (bool, error) {
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	if leftErr != nil && !errors.Is(leftErr, os.ErrNotExist) {
		return false, leftErr
	}
	if rightErr != nil && !errors.Is(rightErr, os.ErrNotExist) {
		return false, rightErr
	}
	if leftErr == nil && rightErr == nil {
		return os.SameFile(leftInfo, rightInfo), nil
	}
	leftPath, err := CanonicalPath(left)
	if err != nil {
		return false, err
	}
	rightPath, err := CanonicalPath(right)
	if err != nil {
		return false, err
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(leftPath, rightPath), nil
	}
	return leftPath == rightPath, nil
}

func CanonicalPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if _, err := os.Lstat(abs); err == nil {
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return "", err
		}
		return filepath.Clean(resolved), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent, suffix := filepath.Dir(abs), []string{filepath.Base(abs)}
	for {
		if _, err := os.Lstat(parent); err == nil {
			resolved, err := filepath.EvalSymlinks(parent)
			if err != nil {
				return "", err
			}
			return filepath.Clean(filepath.Join(append([]string{resolved}, suffix...)...)), nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", fmt.Errorf("no physical parent directory")
		}
		suffix = append([]string{filepath.Base(parent)}, suffix...)
		parent = next
	}
}

func PathWithin(root, candidate string) (bool, error) {
	rootPath, err := CanonicalPath(root)
	if err != nil {
		return false, err
	}
	candidatePath, err := CanonicalPath(candidate)
	if err != nil {
		return false, err
	}
	relative, err := filepath.Rel(rootPath, candidatePath)
	if err != nil {
		return false, err
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))), nil
}
