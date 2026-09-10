package openaicompat

// codeinterpreter runPythonProcess / 进程环境 / 产物收集分支的补充覆盖测试。
// 语义对照 Node 归档 code-interpreter-executor.ts：白名单环境变量、共享输出
// 字节上限、超时 kill、abort 语义与产物省略原因。
//
// 这些用例直接执行真实 python 解释器（进程边界即被测对象，无法用 Mock 替代）；
// 环境缺少 python 时跳过并注明原因。

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func covPythonPath(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("python")
	if err != nil {
		path, err = exec.LookPath("python3")
	}
	if err != nil {
		t.Skipf("未安装 python 解释器；跳过 runPythonProcess 真实进程用例")
	}
	return path
}

func covRealExecutor(t *testing.T, mutate func(*Config)) *CodeInterpreterExecutor {
	t.Helper()
	config := Config{
		HostedToolCodeInterpreterMode: "local_runtime",
		FilesRoot:                     t.TempDir(),
		CodeInterpreter: CodeInterpreterConfig{
			PythonCommand:        covPythonPath(t),
			TimeoutMs:            15000,
			TempRoot:             t.TempDir(),
			CleanupTempDirectory: true,
		},
	}
	if mutate != nil {
		mutate(&config)
	}
	return CodeInterpreterExecutorForRequest(config, newTestStore(t), &CodeInterpreterScope{
		SystemAccountID: testScopeA, APIKeyID: testKeyA,
	})
}

func TestCovRunPythonProcessHappyPath(t *testing.T) {
	// 注意：runner 以 python -I（隔离模式）运行，-I 隐含 -E 会忽略
	// PYTHONIOENCODING/PYTHONUTF8；Windows 上 stdout 会按本地 ANSI 代码页
	// （GBK）编码非 ASCII 文本。这里只用 ASCII 断言管道行为本身。
	executor := covRealExecutor(t, nil)
	result, err := executor.Execute(context.Background(), CodeInterpreterInput{
		Code: "print('stdout-line')\nimport sys\nprint('stderr-line', file=sys.stderr)",
	})
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if result.TimedOut {
		t.Errorf("不应超时：%+v", result)
	}
	if !strings.Contains(result.Stdout, "stdout-line") {
		t.Errorf("stdout = %q", result.Stdout)
	}
	if !strings.Contains(result.Stderr, "stderr-line") {
		t.Errorf("stderr = %q", result.Stderr)
	}
	if result.ExitCode != nil && *result.ExitCode != 0 {
		t.Errorf("exit code = %v", *result.ExitCode)
	}
	if result.OutputTruncated {
		t.Errorf("不应截断")
	}
}

func TestCovRunPythonProcessExitCodeAndTimeout(t *testing.T) {
	t.Run("非零退出码", func(t *testing.T) {
		executor := covRealExecutor(t, nil)
		result, err := executor.Execute(context.Background(), CodeInterpreterInput{
			Code: "raise SystemExit(3)",
		})
		if err != nil {
			t.Fatalf("执行失败：%v", err)
		}
		if result.ExitCode == nil || *result.ExitCode != 3 {
			t.Fatalf("exit code = %v", result.ExitCode)
		}
		if result.Metadata["error_code"] != nil {
			t.Errorf("SystemExit 不算 spawn 失败：%v", result.Metadata)
		}
	})
	t.Run("超时 kill", func(t *testing.T) {
		executor := covRealExecutor(t, func(config *Config) {
			config.CodeInterpreter.TimeoutMs = 300
		})
		result, err := executor.Execute(context.Background(), CodeInterpreterInput{
			Code: "import time\nprint('started', flush=True)\ntime.sleep(10)",
		})
		if err != nil {
			t.Fatalf("执行失败：%v", err)
		}
		if !result.TimedOut {
			t.Fatalf("应超时：%+v", result)
		}
		if !strings.Contains(result.Stdout, "started") {
			t.Errorf("超时前输出应保留：%q", result.Stdout)
		}
	})
	t.Run("输出字节上限截断", func(t *testing.T) {
		executor := covRealExecutor(t, func(config *Config) {
			config.CodeInterpreter.MaxOutputBytes = 64
		})
		result, err := executor.Execute(context.Background(), CodeInterpreterInput{
			Code: "print('x' * 4096, flush=True)",
		})
		if err != nil {
			t.Fatalf("执行失败：%v", err)
		}
		if !result.OutputTruncated {
			t.Fatalf("应截断：%+v", result)
		}
		if int64(len(result.Stdout)) > 64 {
			t.Errorf("stdout 应收敛到上限内：%d", len(result.Stdout))
		}
	})
	t.Run("调用方中途取消 -> aborted", func(t *testing.T) {
		executor := covRealExecutor(t, nil)
		// 200ms 后取消，代码睡眠 5s：模拟调用方在执行中放弃。
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		result, err := executor.Execute(ctx, CodeInterpreterInput{
			Code: "import time\nprint('started', flush=True)\ntime.sleep(5)",
		})
		if err != nil {
			t.Fatalf("执行失败：%v", err)
		}
		if result.TimedOut {
			t.Errorf("配置超时（15s）未到，不应标记 TimedOut：%+v", result)
		}
		if result.Metadata["aborted"] != true {
			t.Fatalf("取消后应标记 aborted：%+v", result)
		}
	})
}

func TestCovRunPythonProcessSpawnFailure(t *testing.T) {
	config := Config{
		HostedToolCodeInterpreterMode: "local_runtime",
		FilesRoot:                     t.TempDir(),
		CodeInterpreter: CodeInterpreterConfig{
			PythonCommand: "definitely-missing-python-xyz",
			TempRoot:      t.TempDir(),
		},
	}
	executor := CodeInterpreterExecutorForRequest(config, newTestStore(t), &CodeInterpreterScope{
		SystemAccountID: testScopeA, APIKeyID: testKeyA,
	})
	result, err := executor.Execute(context.Background(), CodeInterpreterInput{Code: "print('hi')"})
	if err != nil {
		t.Fatalf("spawn 失败应折进结果：%v", err)
	}
	if result.Metadata["error_code"] != "python_spawn_failed" {
		t.Fatalf("metadata = %v", result.Metadata)
	}
	if result.ExitCode == nil || *result.ExitCode != 1 {
		t.Errorf("spawn 失败退出码 = %v", result.ExitCode)
	}
	if !strings.Contains(result.Stderr, "python process failed") {
		t.Errorf("stderr = %q", result.Stderr)
	}
}

func TestCovCodeInterpreterProcessEnv(t *testing.T) {
	env := codeInterpreterProcessEnv()
	keys := make([]string, 0, len(env))
	seen := map[string]string{}
	for _, pair := range env {
		parts := strings.SplitN(pair, "=", 2)
		keys = append(keys, parts[0])
		seen[parts[0]] = parts[1]
	}
	if !sort.StringsAreSorted(keys) {
		t.Errorf("环境变量应按 key 排序：%v", keys)
	}
	if seen["PYTHONIOENCODING"] != "utf-8" || seen["PYTHONUTF8"] != "1" || seen["NO_PROXY"] != "*" {
		t.Errorf("白名单缺省 = %v", seen)
	}
}

func TestCovCodeInterpreterArtifactOmitReasons(t *testing.T) {
	seedRunner := func(executor *CodeInterpreterExecutor, files map[string][]byte) {
		executor.runner = func(ctx context.Context, config CodeInterpreterConfig, runnerPath, codePath, workDir string) interpreterRunResult {
			for name, content := range files {
				if err := os.WriteFile(filepath.Join(workDir, name), content, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			return interpreterRunResult{}
		}
	}
	t.Run("artifact 过大省略", func(t *testing.T) {
		executor := covRealExecutor(t, func(config *Config) {
			config.CodeInterpreter.MaxArtifactBytes = 4
		})
		seedRunner(executor, map[string][]byte{"big.txt": []byte("0123456789")})
		result, err := executor.Execute(context.Background(), CodeInterpreterInput{Code: "pass"})
		if err != nil {
			t.Fatalf("执行失败：%v", err)
		}
		if len(result.Artifacts) != 1 {
			t.Fatalf("artifacts = %+v", result.Artifacts)
		}
		if result.Artifacts[0].ContentOmitted != true || result.Artifacts[0].OmitReason != "file_too_large" {
			t.Errorf("artifact = %+v", result.Artifacts[0])
		}
	})
	t.Run("数量上限省略", func(t *testing.T) {
		executor := covRealExecutor(t, func(config *Config) {
			config.CodeInterpreter.MaxArtifactCount = 2
		})
		seedRunner(executor, map[string][]byte{
			"a.txt": []byte("a"), "b.txt": []byte("b"), "c.txt": []byte("c"), "d.txt": []byte("d"),
		})
		result, err := executor.Execute(context.Background(), CodeInterpreterInput{Code: "pass"})
		if err != nil {
			t.Fatalf("执行失败：%v", err)
		}
		if len(result.Artifacts) != 2 {
			t.Fatalf("应保留 2 个 artifact：%+v", result.Artifacts)
		}
		if result.ArtifactsOmittedCount != 2 {
			t.Errorf("omitted = %d", result.ArtifactsOmittedCount)
		}
	})
	t.Run("缺 gateway scope 省略", func(t *testing.T) {
		config := Config{
			HostedToolCodeInterpreterMode: "local_runtime",
			FilesRoot:                     t.TempDir(),
			CodeInterpreter: CodeInterpreterConfig{
				PythonCommand: covPythonPath(t),
				TempRoot:      t.TempDir(),
			},
		}
		executor := CodeInterpreterExecutorForRequest(config, newTestStore(t), nil)
		seedRunner(executor, map[string][]byte{"out.txt": []byte("内容")})
		result, err := executor.Execute(context.Background(), CodeInterpreterInput{Code: "pass"})
		if err != nil {
			t.Fatalf("执行失败：%v", err)
		}
		if len(result.Artifacts) != 1 || result.Artifacts[0].OmitReason != "missing_gateway_scope" {
			t.Fatalf("artifacts = %+v", result.Artifacts)
		}
	})
	t.Run("DB 写失败 -> persist_failed", func(t *testing.T) {
		store := newTestStore(t)
		executor := covRealExecutor(t, nil)
		// 用独立 executor 绑定该 store 后立即关闭库，触发持久化失败。
		executor.store = store
		if err := store.db.Close(); err != nil {
			t.Fatalf("关闭库失败：%v", err)
		}
		seedRunner(executor, map[string][]byte{"out.txt": []byte("内容")})
		result, err := executor.Execute(context.Background(), CodeInterpreterInput{Code: "pass"})
		if err != nil {
			t.Fatalf("执行失败：%v", err)
		}
		if len(result.Artifacts) != 1 || result.Artifacts[0].OmitReason != "persist_failed" {
			t.Fatalf("artifacts = %+v", result.Artifacts)
		}
	})
	t.Run("containerID 生成容器下载路径", func(t *testing.T) {
		executor := covRealExecutor(t, nil)
		seedRunner(executor, map[string][]byte{"out.txt": []byte("内容")})
		result, err := executor.Execute(context.Background(), CodeInterpreterInput{Code: "pass", ContainerID: "ctr-1"})
		if err != nil {
			t.Fatalf("执行失败：%v", err)
		}
		if len(result.Artifacts) != 1 {
			t.Fatalf("artifacts = %+v", result.Artifacts)
		}
		artifact := result.Artifacts[0]
		if artifact.ContainerID != "ctr-1" ||
			!strings.HasPrefix(artifact.DownloadPath, "/v1/files/") ||
			artifact.ContainerDownloadPath != "/v1/containers/ctr-1/files/"+artifact.FileID+"/content" {
			t.Errorf("artifact = %+v", artifact)
		}
	})
	t.Run("TempRoot 不可创建 -> 报错", func(t *testing.T) {
		blockerFile := filepath.Join(t.TempDir(), "blocker")
		if err := os.WriteFile(blockerFile, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		config := Config{
			HostedToolCodeInterpreterMode: "local_runtime",
			FilesRoot:                     t.TempDir(),
			CodeInterpreter: CodeInterpreterConfig{
				PythonCommand: covPythonPath(t),
				TempRoot:      blockerFile,
			},
		}
		executor := CodeInterpreterExecutorForRequest(config, newTestStore(t), nil)
		if _, err := executor.Execute(context.Background(), CodeInterpreterInput{Code: "pass"}); err == nil {
			t.Fatal("TempRoot 指向文件时 MkdirAll 应失败")
		}
	})
	t.Run("CleanupTempDirectory=false 保留工作目录", func(t *testing.T) {
		executor := covRealExecutor(t, func(config *Config) {
			config.CodeInterpreter.CleanupTempDirectory = false
		})
		var observedWorkDir string
		executor.runner = func(ctx context.Context, config CodeInterpreterConfig, runnerPath, codePath, workDir string) interpreterRunResult {
			observedWorkDir = workDir
			return interpreterRunResult{}
		}
		if _, err := executor.Execute(context.Background(), CodeInterpreterInput{Code: "pass"}); err != nil {
			t.Fatalf("执行失败：%v", err)
		}
		if _, err := os.Stat(observedWorkDir); err != nil {
			t.Errorf("工作目录应保留：%v", err)
		}
	})
}
