package chat

import (
	"fmt"
	"sync"
	"testing"
)

// 并发首命中回归：字段名由请求路径传入（生图响应解析 revised_prompt），多个
// 轮次 goroutine 各自带未缓存字段名进入编译分支。修复前是未加锁的包级 map
// 写入，会触发 fatal error: concurrent map writes（进程级不可恢复退出）。
// 每个 goroutine 用独立字段名，确保命中未缓存的写入路径。
func TestExtractJSONStringFieldConcurrentFirstUse(t *testing.T) {
	const workers = 32
	var waitGroup sync.WaitGroup
	for index := 0; index < workers; index++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			field := fmt.Sprintf("revised_prompt_%d", index)
			body := `{"` + field + `":"value-` + fmt.Sprint(index) + `"}`
			if got := extractJSONStringField(body, field); got != "value-"+fmt.Sprint(index) {
				t.Errorf("field %s = %q", field, got)
			}
		}(index)
	}
	waitGroup.Wait()
}

func TestExtractJSONStringFieldCachedRepeat(t *testing.T) {
	body := `{"revised_prompt":"a cat","other":"ignored"}`
	for index := 0; index < 3; index++ {
		if got := extractJSONStringField(body, "revised_prompt"); got != "a cat" {
			t.Fatalf("run %d = %q", index, got)
		}
	}
	if got := extractJSONStringField(`{"revised_prompt":123}`, "revised_prompt"); got != "" {
		t.Fatalf("non-string value = %q", got)
	}
}
