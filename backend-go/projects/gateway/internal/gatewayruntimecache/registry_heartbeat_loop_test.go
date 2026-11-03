package gatewayruntimecache

// 心跳循环的 timer 释放回归（2026-11-03 内存审查修复）：旧实现把
// `defer timer.Stop()` 写在 for 循环体内，defer 链随运行时长线性增长
// （每 registryHeartbeatEvery 一层）。本测试以静态断言固定"循环内不得出现
// defer timer.Stop()"，避免回退——该缺陷只在长跑后显形（每 5s 一层，数月
// 数万层），功能测试无法覆盖。

import (
	"os"
	"strings"
	"testing"
)

func TestRegistryHeartbeatLoopDoesNotDeferTimerStop(t *testing.T) {
	source, err := os.ReadFile("registry.go")
	if err != nil {
		t.Fatalf("read registry.go: %v", err)
	}
	text := string(source)
	start := strings.Index(text, "func (r *Registry) publishAndScheduleHeartbeat(")
	if start < 0 {
		t.Fatal("publishAndScheduleHeartbeat 不存在（实现已改名，请同步本测试）")
	}
	end := strings.Index(text[start:], "\nfunc ")
	if end < 0 {
		end = len(text) - start
	}
	body := text[start : start+end]

	// 只看代码行：注释里可能提及该反模式（本实现的说明注释就会提到），
	// 静态断言必须忽略注释，否则误报。
	code := make([]string, 0, 32)
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		code = append(code, line)
	}
	codeText := strings.Join(code, "\n")

	if strings.Contains(codeText, "defer timer.Stop()") {
		t.Fatal("心跳循环内不得使用 defer timer.Stop()：defer 链随运行时长线性增长，必须显式 Stop")
	}
	if !strings.Contains(codeText, "timer.Stop()") {
		t.Fatal("心跳循环必须在 stopCh 分支显式 timer.Stop()")
	}
	// 定时器必须在循环体内创建（每轮新 timer），否则 select 会永久阻塞。
	if !strings.Contains(codeText, "time.NewTimer(registryHeartbeatEvery)") {
		t.Fatal("心跳循环必须每轮创建新 timer")
	}
}
