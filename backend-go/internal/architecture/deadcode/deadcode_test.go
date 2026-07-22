package deadcode

import (
	"context"
	"flag"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

const modulePath = "juhe-ai/backend-go"

var strict = flag.Bool("deadcode-strict", false, "fail when business packages are not reachable from a cmd entrypoint")

var knownUnregisteredPackages = []string{
	"juhe-ai/backend-go/db/migrationtests",
	"juhe-ai/backend-go/internal/architecture/deadcode",
	"juhe-ai/backend-go/internal/jobs/accountbalanceautodetect",
	"juhe-ai/backend-go/internal/jobs/accountbalancerefresh",
	"juhe-ai/backend-go/internal/jobs/accountbalancesnapshotcleanup",
	"juhe-ai/backend-go/internal/jobs/accounthealthcheck",
	"juhe-ai/backend-go/internal/jobs/cooldownaccountretest",
	"juhe-ai/backend-go/internal/modules/accountbalanceautodetect",
	"juhe-ai/backend-go/internal/modules/accountbalancesnapshotcleanup",
	"juhe-ai/backend-go/internal/modules/accounthealthcheck",
	"juhe-ai/backend-go/internal/modules/cooldownaccountretest",
}

var strictAllowedPackages = []string{
	"juhe-ai/backend-go/db/migrationtests",
	"juhe-ai/backend-go/internal/architecture/deadcode",
}

func TestCommandEntrypointReachability(t *testing.T) {
	moduleRoot := findModuleRoot(t)

	allPackages := goList(t, moduleRoot, "-mod=readonly", "-f", "{{.ImportPath}}", "./...")
	commandDependencies := goList(t, moduleRoot, "-mod=readonly", "-deps", "-f", "{{.ImportPath}}", "./cmd/...")
	unregistered := packageDifference(modulePackages(allPackages), modulePackages(commandDependencies))

	if *strict {
		unexpected := packageDifference(unregistered, strictAllowedPackages)
		if len(unexpected) > 0 {
			t.Fatalf("严格死代码门禁发现 %d 个未被 cmd 入口引用的业务包：\n%s", len(unexpected), formatPackageList(unexpected))
		}
		return
	}

	unknown := packageDifference(unregistered, knownUnregisteredPackages)
	missing := packageDifference(knownUnregisteredPackages, unregistered)
	if len(unknown) > 0 || len(missing) > 0 {
		t.Fatalf(
			"默认死代码基线发生漂移：\n未知新增：\n%s\n已知缺失：\n%s",
			formatPackageList(unknown),
			formatPackageList(missing),
		)
	}
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位 deadcode 测试文件")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", ".."))
}

func goList(t *testing.T, moduleRoot string, args ...string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", append([]string{"list"}, args...)...)
	cmd.Dir = moduleRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s 失败：%v\n%s", strings.Join(append([]string{"list"}, args...), " "), err, output)
	}
	return strings.Fields(string(output))
}

func modulePackages(packages []string) []string {
	filtered := make([]string, 0, len(packages))
	for _, packagePath := range packages {
		if packagePath == modulePath || strings.HasPrefix(packagePath, modulePath+"/") {
			filtered = append(filtered, packagePath)
		}
	}
	slices.Sort(filtered)
	return slices.Compact(filtered)
}

func packageDifference(left, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, packagePath := range right {
		rightSet[packagePath] = struct{}{}
	}
	difference := make([]string, 0)
	for _, packagePath := range left {
		if _, ok := rightSet[packagePath]; !ok {
			difference = append(difference, packagePath)
		}
	}
	slices.Sort(difference)
	return difference
}

func formatPackageList(packages []string) string {
	if len(packages) == 0 {
		return "- 无"
	}
	return fmt.Sprintf("- %s", strings.Join(packages, "\n- "))
}
