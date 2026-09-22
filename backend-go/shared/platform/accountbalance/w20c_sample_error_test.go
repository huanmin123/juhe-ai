package accountbalance

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// w20c：J2 批跑失败明细此前只在内存 Errors map 中，运行日志仅有计数，
// 测试环境无法定位 errors>0 的原因。logSampleError 必须输出样本失败
// （账户 ID + 截断错误文本），空 Errors 时不得输出。
func TestW20cLogSampleError(t *testing.T) {
	buffer := &bytes.Buffer{}
	service := &Service{logger: slog.New(slog.NewTextHandler(buffer, nil))}

	service.logSampleError("J2 周期余额刷新样本失败", RunReport{Errors: map[string]error{}})
	if buffer.Len() != 0 {
		t.Fatalf("空 Errors 不应输出：%s", buffer.String())
	}

	long := strings.Repeat("字", 400)
	service.logSampleError("J2 周期余额刷新样本失败", RunReport{
		Errors: map[string]error{"acc_w20c_a": errors.New(long)},
	})
	output := buffer.String()
	if !strings.Contains(output, "acc_w20c_a") {
		t.Fatalf("样本日志应包含账户 ID：%s", output)
	}
	if !strings.Contains(output, "J2 周期余额刷新样本失败") {
		t.Fatalf("样本日志应包含消息：%s", output)
	}
	if runeCount := len([]rune(output)); strings.Count(output, "字") > 300 {
		t.Fatalf("错误文本应截断到 300 rune：rune 总数 %d", runeCount)
	}
}
