package openaicompat

// filestorage.go 纯函数分支的补充覆盖：存储 key 形态、路径逃逸拒绝、
// 媒体类型表与上传文件名归一化。

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCovStorageKeyForFileShapes(t *testing.T) {
	t.Run("分片与清洗", func(t *testing.T) {
		key := StorageKeyForFile("file-abcdef123456789")
		parts := strings.Split(key, "/")
		if len(parts) != 3 || parts[0] != "files" || parts[1] != "file-abc" {
			t.Errorf("存储 key 分片 = %q", key)
		}
		// safeStorageSegment 对空串回退 "file"，shard 随之是 "file"
		// （"default" 分支在当前实现下不可达，仅留作 Node 对照）。
		if got := StorageKeyForFile(""); got != "files/file/file" {
			t.Errorf("空 id 应回退 file 段：%q", got)
		}
		if got := StorageKeyForFile("a/b\\c:d"); !strings.Contains(got, "a_b_c_d") {
			t.Errorf("不安全字符应替换：%q", got)
		}
	})
	t.Run("超长段截断", func(t *testing.T) {
		long := strings.Repeat("a", 200)
		if got := safeStorageSegment(long); len(got) != 160 {
			t.Errorf("safeStorageSegment 应截断 160：%d", len(got))
		}
	})
}

func TestCovFileObjectPathBranches(t *testing.T) {
	root := t.TempDir()
	t.Run("正常相对 key", func(t *testing.T) {
		path, err := FileObjectPath(root, "files/ab/cd.txt")
		if err != nil {
			t.Fatalf("解析失败：%v", err)
		}
		if _, err := filepath.Rel(root, path); err != nil {
			t.Errorf("路径应位于 root 下：%q", path)
		}
	})
	t.Run("根锚定 key 拒绝", func(t *testing.T) {
		for _, key := range []string{"/abs.txt", "\\abs.txt", "C:evil.txt", filepath.FromSlash("/x/y")} {
			if _, err := FileObjectPath(root, key); !errors.Is(err, StorageKeyEscapeError) {
				t.Errorf("根锚定 key %q 应拒绝，实际 %v", key, err)
			}
		}
	})
	t.Run("向上逃逸拒绝", func(t *testing.T) {
		for _, key := range []string{"../escape.txt", "files/../../escape.txt", ".", ""} {
			if _, err := FileObjectPath(root, key); !errors.Is(err, StorageKeyEscapeError) {
				t.Errorf("逃逸 key %q 应拒绝，实际 %v", key, err)
			}
		}
	})
	t.Run("EnsureFileObjectParent 创建目录", func(t *testing.T) {
		path, err := EnsureFileObjectParent(root, "files/shard1/obj.txt")
		if err != nil {
			t.Fatalf("创建失败：%v", err)
		}
		if _, err := filepath.Rel(root, path); err != nil {
			t.Errorf("路径应位于 root 下：%q", path)
		}
	})
	t.Run("RemoveFileObject 缺失不算错误", func(t *testing.T) {
		if err := RemoveFileObject(root, "files/shard1/gone.txt"); err != nil {
			t.Errorf("缺失文件不应报错：%v", err)
		}
	})
}

func TestCovMediaTypeFromFilenameTable(t *testing.T) {
	cases := map[string]string{
		"a.PDF":      "application/pdf",
		"b.txt":      "text/plain",
		"c.md":       "text/markdown",
		"d.markdown": "text/markdown",
		"e.csv":      "text/csv",
		"f.json":     "application/json",
		"g.c":        "text/x-c",
		"h.cpp":      "text/x-c++",
		"i.cc":       "text/x-c++",
		"j.cxx":      "text/x-c++",
		"k.cs":       "text/x-csharp",
		"l.css":      "text/css",
		"m.go":       "text/x-golang",
		"n.html":     "text/html",
		"o.htm":      "text/html",
		"p.java":     "text/x-java",
		"q.js":       "text/javascript",
		"r.mjs":      "text/javascript",
		"s.cjs":      "text/javascript",
		"t.php":      "text/x-php",
		"u.py":       "text/x-python",
		"v.rb":       "text/x-ruby",
		"w.tex":      "text/x-tex",
		"x.ts":       "application/typescript",
		"y.tsx":      "application/typescript",
		"z.sh":       "application/x-sh",
		"1.png":      "image/png",
		"2.jpg":      "image/jpeg",
		"3.jpeg":     "image/jpeg",
		"4.gif":      "image/gif",
		"5.webp":     "image/webp",
		"6.unknown":  "",
		"":           "",
	}
	for filename, want := range cases {
		if got := MediaTypeFromFilename(filename); got != want {
			t.Errorf("MediaTypeFromFilename(%q) = %q，期望 %q", filename, got, want)
		}
	}
}

func TestCovNormalizeFileMediaTypeAndUploadFilename(t *testing.T) {
	t.Run("NormalizeFileMediaType", func(t *testing.T) {
		if got := NormalizeFileMediaType("IMAGE/PNG; charset=binary", "x.bin"); got != "image/png" {
			t.Errorf("显式类型 = %q", got)
		}
		if got := NormalizeFileMediaType("application/octet-stream", "a.csv"); got != "text/csv" {
			t.Errorf("octet-stream 回退文件名 = %q", got)
		}
		if got := NormalizeFileMediaType("", "a.txt"); got != "text/plain" {
			t.Errorf("空类型回退 = %q", got)
		}
		if got := NormalizeFileMediaType("  ", "noext"); got != "" {
			t.Errorf("无法识别 = %q", got)
		}
	})
	t.Run("NormalizedUploadFilename", func(t *testing.T) {
		cases := map[string]string{
			"a/b/c.txt":      "c.txt",
			"a\\b\\d.png":    "d.png",
			"  spaced.png  ": "spaced.png",
			"":               "upload",
			"   ":            "upload",
			"dir/":           "upload",
			"mixed/slash\\x": "x",
		}
		for in, want := range cases {
			if got := NormalizedUploadFilename(in); got != want {
				t.Errorf("NormalizedUploadFilename(%q) = %q，期望 %q", in, got, want)
			}
		}
	})
	t.Run("NewFileID 导出形状", func(t *testing.T) {
		now := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
		if got := NewFileID(now); !strings.HasPrefix(got, "file-") {
			t.Errorf("NewFileID = %q", got)
		}
	})
}
