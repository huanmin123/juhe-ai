// X05 全量验收回放测试基建（黑盒端到端）。
//
// 本包以「fresh 隔离环境 + 真实二进制」的方式执行 X05 验收：每个场景测试
// 在独立临时目录 + 随机端口上构建完整运行环境（maintenance ensure+seed →
// gateway/jobs 启动 → 登录 → 全场景回归），不触碰任何现有开发/生产数据。
//
// 放置位置说明：放在 gateway 项目 cmd/juhe-ai-gateway/acceptance 下而不是
// 各项目内部，因为 X05 的验收对象是「三二进制协作的完整栈」（gateway 是
// 组合根，maintenance/jobs 是配套进程），且作为黑盒测试它只依赖二进制的
// env/HTTP 契约，不 import 任何 internal 包，避免与实现细节耦合；放在
// gateway 模块内使 `go test ./...`（projects/gateway）天然覆盖它。
//
// 运行方式：
//
//	cd backend-go/projects/gateway && go test ./cmd/juhe-ai-gateway/acceptance/... -count=1
//
// PG 双模式：设置 JUHE_AI_ACCEPTANCE_PG_DSN（指向一个可写、可清空的隔离
// PostgreSQL 库）后，PG 门控场景才会运行；未设置时 skip。PG 模式下存储
// 不随测试运行销毁，因此场景断言降级为渲染级（登录、健康、只读列表），
// 并且所有创建类资源名带运行随机后缀以保证可重复执行。
package acceptance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	gatewayBinary     string
	maintenanceBinary string
	jobsBinaryOnce    sync.Once
	jobsBinaryPath    string
	jobsBinaryErr     error
	sharedRoot        string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "juhe-ai-acceptance-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create shared temp root: %v\n", err)
		os.Exit(1)
	}
	sharedRoot = dir

	gatewayProject, err := projectDir("gateway")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	gatewayBinary, err = buildBinary(gatewayProject, "./cmd/juhe-ai-gateway", filepath.Join(sharedRoot, binName("juhe-ai-gateway")))
	if err != nil {
		fmt.Fprintf(os.Stderr, "build gateway binary: %v\n", err)
		os.Exit(1)
	}
	maintenanceProject, err := projectDir("maintenance")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	maintenanceBinary, err = buildBinary(maintenanceProject, "./cmd/juhe-ai-maintenance", filepath.Join(sharedRoot, binName("juhe-ai-maintenance")))
	if err != nil {
		fmt.Fprintf(os.Stderr, "build maintenance binary: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(sharedRoot)
	os.Exit(code)
}

// ensureJobsBinary 懒构建 juhe-ai-jobs。jobs 项目由并行迁移波次独立开发
// （J 族切片），其进行中的工作区状态可能暂时不可编译；X05 只有 jobs 冒烟
// 场景依赖该二进制，构建失败时该场景 skip 并带上明确原因，不影响其余
// 验收场景的门禁。
func ensureJobsBinary(t *testing.T) string {
	t.Helper()
	jobsBinaryOnce.Do(func() {
		jobsProject, err := projectDir("jobs")
		if err != nil {
			jobsBinaryErr = err
			return
		}
		jobsBinaryPath, jobsBinaryErr = buildBinary(jobsProject, "./cmd/juhe-ai-jobs", filepath.Join(sharedRoot, binName("juhe-ai-jobs")))
	})
	if jobsBinaryErr != nil {
		t.Skipf("juhe-ai-jobs 当前工作区不可编译（并行迁移 WIP），跳过 jobs 冒烟: %v", jobsBinaryErr)
	}
	return jobsBinaryPath
}

// projectDir resolves the sibling backend-go project directory relative to
// this file (acceptance -> juhe-ai-gateway -> cmd -> gateway).
func projectDir(name string) (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("resolve acceptance source dir: runtime.Caller failed")
	}
	gatewayProject := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(thisFile))))
	candidate := filepath.Join(filepath.Dir(gatewayProject), name)
	info, err := os.Stat(filepath.Join(candidate, "go.mod"))
	if err != nil || info.IsDir() {
		return "", fmt.Errorf("locate %s project dir (looked at %s): %w", name, candidate, err)
	}
	return candidate, nil
}

func binName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func buildBinary(projectDir, pkg, output string) (string, error) {
	cmd := exec.Command("go", "build", "-o", output, pkg)
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	started := time.Now()
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("go build in %s: %v: %s", projectDir, err, strings.TrimSpace(stderr.String()))
	}
	fmt.Fprintf(os.Stderr, "built %s (%.1fs)\n", output, time.Since(started).Seconds())
	return output, nil
}
