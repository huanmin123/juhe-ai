package managementruntimeloggrep

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	rgProcessHelperPrefix    = "juhe-rg-process-helper-"
	rgProcessHelperArgPrefix = "--juhe-rg-process-helper="
)

func TestMain(m *testing.M) {
	if mode, ok := rgProcessHelperMode(os.Args); ok {
		os.Exit(runRGProcessHelper(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func TestServiceRealRGHandlesSpacedPathRotationAndDeletion(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg is not installed")
	}

	directory := filepath.Join(t.TempDir(), "runtime logs with spaces")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	activePath := filepath.Join(directory, "juhe-ai.log")
	writeRuntimeLogLines(t, activePath,
		`{"time":"2026-07-22T11:00:00Z","level":30,"event":"before_rotation","msg":"rotationneedle"}`,
	)
	rotatedPath := filepath.Join(directory, "juhe-ai.server.20260722T110000Z.log")
	if err := os.Rename(activePath, rotatedPath); err != nil {
		t.Fatal(err)
	}
	writeRuntimeLogLines(t, activePath,
		`{"time":"2026-07-22T11:01:00Z","level":30,"event":"after_rotation","msg":"rotationneedle"}`,
	)

	service := NewService(Options{Directory: directory, FileEnabled: true, MaxFiles: 10})
	result := service.Grep(context.Background(), Input{Keywords: []string{"rotationneedle"}, Limit: 10})
	if !result.Available || result.ScannedFileCount != 2 || len(result.Items) != 2 {
		t.Fatalf("rotated result = %+v", result)
	}
	if result.Items[0].Event != "after_rotation" || result.Items[1].Event != "before_rotation" {
		t.Fatalf("rotated item order = %+v", result.Items)
	}
	for _, item := range result.Items {
		if strings.Contains(item.File, directory) || strings.Contains(item.ID, directory) {
			t.Fatalf("spaced filesystem path leaked: %+v", item)
		}
	}

	if err := os.Remove(rotatedPath); err != nil {
		t.Fatal(err)
	}
	result = service.Grep(context.Background(), Input{Keywords: []string{"rotationneedle"}, Limit: 10})
	if !result.Available || result.ScannedFileCount != 1 || len(result.Items) != 1 || result.Items[0].Event != "after_rotation" {
		t.Fatalf("post-delete result = %+v", result)
	}
}

func TestListLogFilesExcludesSymlinksAndNonRegularEntries(t *testing.T) {
	directory := t.TempDir()
	regularPath := filepath.Join(directory, "regular.log")
	writeRuntimeLogLines(t, regularPath,
		`{"time":"2026-07-22T11:00:00Z","level":30,"event":"regular","msg":"listneedle"}`,
	)
	if err := os.Mkdir(filepath.Join(directory, "directory.log"), 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(directory, "symlink.log")
	if err := os.Symlink(regularPath, symlinkPath); err != nil {
		t.Skipf("symlink creation unavailable on this host: %v", err)
	}

	service := NewService(Options{Directory: directory, FileEnabled: true, MaxFiles: 10, RGPath: "rg"})
	listing, err := service.listLogFiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.files) != 1 || listing.files[0].name != "regular.log" {
		t.Fatalf("listed files = %+v", listing.files)
	}
}

func TestRunRGCommandDrainsAndRedactsStderr(t *testing.T) {
	helper := copyRGProcessHelper(t, "stderr")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	started := time.Now()
	state, err := runRGCommand(ctx, helper, []string{rgProcessHelperArgPrefix + "stderr"}, func([]byte) bool {
		t.Fatal("stderr helper must not emit stdout events")
		return true
	})
	if state != rgFailed || err == nil || err.Error() != "rg execution failed" {
		t.Fatalf("state=%v err=%v", state, err)
	}
	if time.Since(started) > 8*time.Second {
		t.Fatalf("stderr helper was not drained promptly: %s", time.Since(started))
	}
	if strings.Contains(err.Error(), "stderr-secret") {
		t.Fatalf("stderr leaked through service error: %v", err)
	}
}

func TestRunRGCommandDeadlineAndCancelWaitForChildExit(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		context   func() (context.Context, context.CancelFunc)
		wantState rgExitState
		wantErr   error
	}{
		{
			name: "deadline",
			context: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), time.Second)
			},
			wantState: rgTimeout,
		},
		{
			name: "cancel",
			context: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			wantState: rgFailed,
			wantErr:   context.Canceled,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			helper := copyRGProcessHelper(t, "sleep")
			markerPath := filepath.Join(t.TempDir(), "child.log")
			ctx, cancel := testCase.context()
			defer cancel()
			if testCase.name == "cancel" {
				go func() {
					_ = waitForFile(markerPath+".pid", 2*time.Second)
					cancel()
				}()
			}

			started := time.Now()
			state, runErr := runRGCommand(ctx, helper, []string{rgProcessHelperArgPrefix + "sleep", "--", "needle", markerPath}, func([]byte) bool { return true })
			if state != testCase.wantState {
				t.Fatalf("state = %v, want %v", state, testCase.wantState)
			}
			if !errors.Is(runErr, testCase.wantErr) || (testCase.wantErr == nil && runErr != nil) {
				t.Fatalf("error = %v, want %v", runErr, testCase.wantErr)
			}
			if time.Since(started) > 8*time.Second {
				t.Fatalf("canceled helper did not exit promptly: %s", time.Since(started))
			}
			pidText, err := os.ReadFile(markerPath + ".pid")
			if err != nil {
				t.Fatalf("helper did not start: %v", err)
			}
			if _, err := strconv.Atoi(strings.TrimSpace(string(pidText))); err != nil {
				t.Fatalf("invalid helper pid %q: %v", pidText, err)
			}
			// Windows refuses to remove a running executable. On every platform,
			// runRGCommand returning only after Wait makes this a useful cleanup guard.
			if err := os.Remove(helper); err != nil {
				t.Fatalf("helper process still owns executable after return: %v", err)
			}
		})
	}
}

func rgProcessHelperMode(args []string) (string, bool) {
	for _, arg := range args[1:] {
		if mode, ok := strings.CutPrefix(arg, rgProcessHelperArgPrefix); ok && mode != "" {
			return mode, true
		}
	}
	name := strings.TrimSuffix(filepath.Base(args[0]), filepath.Ext(args[0]))
	mode, ok := strings.CutPrefix(name, rgProcessHelperPrefix)
	return mode, ok && mode != ""
}

func runRGProcessHelper(mode string, args []string) int {
	switch mode {
	case "stderr":
		_, _ = io.WriteString(os.Stderr, strings.Repeat("stderr-secret-", 32*1024))
		return 2
	case "sleep":
		if len(args) == 0 {
			return 3
		}
		markerPath := args[len(args)-1] + ".pid"
		if err := os.WriteFile(markerPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			return 4
		}
		for {
			time.Sleep(time.Second)
		}
	default:
		return 5
	}
}

func copyRGProcessHelper(t *testing.T, mode string) string {
	t.Helper()
	sourcePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	destinationPath := filepath.Join(t.TempDir(), rgProcessHelperPrefix+mode+filepath.Ext(sourcePath))
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		t.Fatal(err)
	}
	if err := destination.Close(); err != nil {
		t.Fatal(err)
	}
	return destinationPath
}

func waitForFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return context.DeadlineExceeded
}

func writeRuntimeLogLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
