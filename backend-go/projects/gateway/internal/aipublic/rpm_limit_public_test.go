package aipublic

// 公开 API 每分钟请求上限（AI账户每分钟请求上限设计 §6.4）最小回归：
// add/update 白名单、0..1000000 解析（0 = 不限制）、列表投影 rpmLimit。

import (
	"net/http"
	"strings"
	"testing"
)

// rpmPublicBody 以独立 name/apiKey 构造公开新增载荷（避免 %s 字面量名称触发
// owner+provider+group+name 重名 409），extra 为追加字段片段。
func rpmPublicBody(name, apiKey, extra string) string {
	base := strings.Replace(wcAccountBase, "%s", name, 1)
	base = strings.Replace(base, "%s", apiKey, 1)
	return strings.Replace(base, `]}`, `],`+extra+`}`, 1)
}

// TestRPMLimitPublicAccountAdd 锁定公开新增面：0 与上界 1000000 合法、
// 负数/超上界/浮点 400、null 视为未提供、缺省落 0 且列表投影恒输出 rpmLimit。
func TestRPMLimitPublicAccountAdd(t *testing.T) {
	env := newWCLifeEnv(t)

	// 缺省（无 rpmLimit 键）→ 落 0，列表投影回显。
	defaultID := wcAddAccount(t, env, rpmPublicBody("rpm默认", "sk-rpm-default", `"status":"active"`))
	status, payload, _ := env.doAuth(http.MethodGet, Prefix+"/account/list?targetUsername=pusher&providerCode=gpt", "", wcLifeToken)
	if status != http.StatusOK {
		t.Fatalf("列表应 200：%d %v", status, payload)
	}
	items := payload["data"].(map[string]any)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != defaultID {
		t.Fatalf("列表应命中新建账号：%v", items)
	}
	if got := items[0].(map[string]any)["rpmLimit"]; got != float64(0) {
		t.Fatalf("缺省 rpmLimit 投影应为 0：%v", items[0])
	}

	// 显式 0 与上界 1000000 合法。
	if status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/account/add",
		rpmPublicBody("rpm零", "sk-rpm-zero", `"status":"active","rpmLimit":0`), wcLifeToken); status != http.StatusCreated {
		t.Fatalf("rpmLimit 0 应合法：%d %v", status, payload)
	}
	if status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/account/add",
		rpmPublicBody("rpm上界", "sk-rpm-max", `"status":"active","rpmLimit":1000000`), wcLifeToken); status != http.StatusCreated {
		t.Fatalf("rpmLimit 1000000 应合法：%d %v", status, payload)
	}

	// 负数与超上界 400（body 解析先于重名检查，形态即拒）。
	for name, body := range map[string]string{
		"负数":  rpmPublicBody("rpm负", "sk-rpm-neg", `"status":"active","rpmLimit":-1`),
		"超上界": rpmPublicBody("rpm超", "sk-rpm-over", `"status":"active","rpmLimit":1000001`),
		"浮点":  rpmPublicBody("rpm浮点", "sk-rpm-float", `"status":"active","rpmLimit":1.5`),
	} {
		status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/account/add", body, wcLifeToken)
		if status != http.StatusBadRequest {
			t.Fatalf("%s 应 400：%d %v", name, status, payload)
		}
	}

	// null 与 add 面其他数值字段同语义（视为未提供，放行）。
	if status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/account/add",
		rpmPublicBody("rpm空", "sk-rpm-null", `"status":"active","rpmLimit":null`), wcLifeToken); status != http.StatusCreated {
		t.Fatalf("add 面 null 应视为未提供：%d %v", status, payload)
	}
}

// TestRPMLimitPublicAccountUpdate 锁定公开更新面：正整数更新、0 = 解除限制、
// 缺失键不改、null 按类型错误拒绝、负数 400、未知键拒绝（白名单外）。
func TestRPMLimitPublicAccountUpdate(t *testing.T) {
	env := newWCLifeEnv(t)
	accountID := wcAddAccount(t, env, rpmPublicBody("rpm更新", "sk-rpm-update", `"status":"active","rpmLimit":500`))

	update := func(body string) (int, map[string]any) {
		status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/account/update",
			`{"accountId":"`+accountID+`",`+body, wcLifeToken)
		return status, payload
	}

	// 正整数更新。
	if status, payload := update(`"rpmLimit":800}`); status != http.StatusOK {
		t.Fatalf("更新 800 应成功：%d %v", status, payload)
	}
	if items := rpmPublicListItems(t, env); items[accountID] != float64(800) {
		t.Fatalf("更新后 rpmLimit 应为 800：%v", items)
	}

	// 显式 0 = 解除限制。
	if status, payload := update(`"rpmLimit":0}`); status != http.StatusOK {
		t.Fatalf("更新 0 应成功：%d %v", status, payload)
	}
	if items := rpmPublicListItems(t, env); items[accountID] != float64(0) {
		t.Fatalf("更新 0 后 rpmLimit 应为 0：%v", items)
	}

	// 缺失键不改。
	if status, payload := update(`"name":"rpm改名"}`); status != http.StatusOK {
		t.Fatalf("改名应成功：%d %v", status, payload)
	}
	if items := rpmPublicListItems(t, env); items[accountID] != float64(0) {
		t.Fatalf("缺失键不应改动 rpmLimit：%v", items)
	}

	// null 按类型错误拒绝；负数 400；未知键拒绝（白名单外）。
	if status, payload := update(`"rpmLimit":null}`); status != http.StatusBadRequest {
		t.Fatalf("null 应 400：%d %v", status, payload)
	}
	if status, payload := update(`"rpmLimit":-2}`); status != http.StatusBadRequest {
		t.Fatalf("负数应 400：%d %v", status, payload)
	}
	if status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/account/update",
		`{"accountId":"`+accountID+`","rpmLimit":10,"bogusKey":1}`, wcLifeToken); status != http.StatusBadRequest {
		t.Fatalf("未知键应 400：%d %v", status, payload)
	}
}

func rpmPublicListItems(t *testing.T, env *aipublicEnv) map[string]float64 {
	t.Helper()
	status, payload, _ := env.doAuth(http.MethodGet, Prefix+"/account/list?targetUsername=pusher&providerCode=gpt", "", wcLifeToken)
	if status != http.StatusOK {
		t.Fatalf("列表应 200：%d %v", status, payload)
	}
	items := payload["data"].(map[string]any)["items"].([]any)
	out := map[string]float64{}
	for _, raw := range items {
		item := raw.(map[string]any)
		value, ok := item["rpmLimit"].(float64)
		if !ok {
			t.Fatalf("列表行缺少 rpmLimit number 投影：%v", item)
		}
		out[item["id"].(string)] = value
	}
	return out
}
