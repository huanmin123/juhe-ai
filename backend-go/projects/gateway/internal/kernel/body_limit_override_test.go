package kernel

// 路由级 body limit 覆盖（RegisterBodyLimitOverride）单测：默认上限零变化、
// 覆盖条目精确生效（方法×路径双维度）、穿过 bodyLimitMiddleware 的装配级验
// 证。大请求体只在测试进程内存中构造，不触网。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestRouteBodyLimitOverride 覆盖条目生效面：注册 POST 覆盖（20MB，演练场参
// 考图上传同款）后——
//  1. 12MB+ 请求体穿过默认 256KiB 限制成功到达 handler；
//  2. 超覆盖值的请求体仍被拒（413 请求体过大）；
//  3. 未注册覆盖的路由维持默认上限（行为零变化）；
//  4. 同路径不同方法不享受覆盖（精确匹配）。
func TestRouteBodyLimitOverride(t *testing.T) {
	k := newTestKernel(t, nil)
	const overrideLimit = 20 * 1024 * 1024
	const overridePath = "/__aisys__/api/playground/images/references"
	echo := func(w http.ResponseWriter, r *http.Request) {
		var target map[string]any
		if DecodeJSON(w, r, &target) {
			WriteOK(w, map[string]int{"len": len(target["blob"].(string))}, "")
		}
	}
	k.RegisterFunc("POST "+overridePath, echo)
	k.RegisterFunc("GET "+overridePath, echo)
	k.RegisterFunc("POST /__aisys__/api/decode", echo)
	k.RegisterBodyLimitOverride(http.MethodPost, overridePath, overrideLimit)
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	// 1. 12MB+ 请求成功（默认 256KiB 下必拒，覆盖值 20MB 放行）。
	blob := strings.Repeat("x", 12*1024*1024)
	response, err := http.Post(server.URL+overridePath, "application/json", strings.NewReader(`{"blob":"`+blob+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("12MB 请求应穿过覆盖上限: status=%d body=%s", response.StatusCode, raw)
	}
	if !strings.Contains(string(raw), `"len":`+strconv.Itoa(12*1024*1024)) {
		t.Fatalf("12MB 请求体应完整到达 handler: %s", raw)
	}

	// 2. 超覆盖值 21MB 拒绝 413。
	oversized := strings.Repeat("x", 21*1024*1024)
	response, err = http.Post(server.URL+overridePath, "application/json", strings.NewReader(`{"blob":"`+oversized+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(string(raw), "请求体过大") {
		t.Fatalf("超覆盖值应 413: status=%d body=%s", response.StatusCode, raw)
	}

	// 3. 未注册路由维持默认 256KiB（零变化）。
	defaultRoute := strings.Repeat("x", 300*1024)
	response, err = http.Post(server.URL+"/__aisys__/api/decode", "application/json", strings.NewReader(`{"blob":"`+defaultRoute+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("未覆盖路由应维持默认上限: status=%d body=%s", response.StatusCode, raw)
	}

	// 4. 同路径 GET 不享受 POST 覆盖。
	request, err := http.NewRequest(http.MethodGet, server.URL+overridePath, strings.NewReader(`{"blob":"`+defaultRoute+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("方法不匹配不得享受覆盖: status=%d body=%s", response.StatusCode, raw)
	}
}
