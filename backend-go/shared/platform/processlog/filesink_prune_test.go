package processlog

// 2026-11-09：运行日志不再由 jobs F1 清理，写侧必须在轮转后自清理本实例的
// rotated 文件；本测试钉住 MaxFiles / Retention 与跨实例隔离。

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileSinkPrunesRotatedFilesOnStartup(t *testing.T) {
	dir := t.TempDir()
	base := "juhe-ai.gw1"
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	makeRotated := func(token string, mod time.Time) string {
		path := filepath.Join(dir, base+".20260101T000000Z."+token+".log")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
		return path
	}
	newest := makeRotated("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", now.Add(-time.Hour))
	older := makeRotated("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", now.Add(-2*time.Hour))
	expired := makeRotated("cccccccc-cccc-cccc-cccc-cccccccccccc", now.Add(-48*time.Hour))
	otherInstance := filepath.Join(dir, "juhe-ai.gw2.20260101T000000Z.dddddddd-dddd-dddd-dddd-dddddddddddd.log")
	if err := os.WriteFile(otherInstance, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	sink, err := NewFileSink(dir, FileSinkOptions{
		FileName:  base + ".log",
		MaxFiles:  1,
		Retention: 24 * time.Hour,
		Now:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()

	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatalf("过期 rotated 必须删除: %v", err)
	}
	if _, err := os.Stat(older); !os.IsNotExist(err) {
		t.Fatalf("超过 MaxFiles 的最旧 rotated 必须删除: %v", err)
	}
	if _, err := os.Stat(newest); err != nil {
		t.Fatalf("最新 rotated 必须保留: %v", err)
	}
	if _, err := os.Stat(otherInstance); err != nil {
		t.Fatalf("其它实例日志必须保留: %v", err)
	}
}
