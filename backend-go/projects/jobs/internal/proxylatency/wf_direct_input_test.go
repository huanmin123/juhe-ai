package proxylatency

import (
	"context"
	"database/sql/driver"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

// 本文件覆盖 PostgresDirectInputReader：只读事务装配、契约预检、候选分页
// 与逐代理聚合、无效代理隔离跳过，以及 PG 标量解码/目标/信封等纯函数契约。
// 全部走录制驱动，SQL 文本与参数逐项断言，不依赖真实 PostgreSQL。

// wfOpenDirectReader 打开固定时钟的直读 reader。
func wfOpenDirectReader(t *testing.T, rec *wfRecorder) *PostgresDirectInputReader {
	t.Helper()
	db := wfOpenRecorderDB(t, rec)
	reader, err := NewPostgresDirectInputReader(db, 5*time.Minute, func() time.Time { return wfProjBase })
	if err != nil {
		t.Fatalf("构造 reader 失败: %v", err)
	}
	return reader
}

// wfTestEnvelope 构造结构合法的 v1 password envelope（nonce 12B/tag 16B）。
func wfTestEnvelope() string {
	nonce := base64.RawURLEncoding.EncodeToString([]byte("123456789012"))
	tag := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef"))
	ciphertext := base64.RawURLEncoding.EncodeToString([]byte("cipher-bytes"))
	return "v1:" + nonce + ":" + tag + ":" + ciphertext
}

// wfCandidateColumns 是候选查询的占位列名（12 列，与行值一一对应）。
func wfCandidateColumns() []string {
	return []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9", "c10", "c11", "c12"}
}

// wfCandidateRow 构造一行候选（列序与 proxyLatencyCandidatesSQL 一致）。
func wfCandidateRow(proxyID string, overrides ...func([]driver.Value)) []driver.Value {
	row := []driver.Value{
		proxyID, "http", "10.0.0.1", int64(8080), "user", "", true,
		wfProjRevision, nil, "gpt", "profile-gpt", "https://api.openai.com/v1",
	}
	for _, override := range overrides {
		override(row)
	}
	return row
}

func TestWFNewPostgresDirectInputReader(t *testing.T) {
	if _, err := NewPostgresDirectInputReader(nil, time.Minute, nil); err == nil {
		t.Fatal("缺数据库必须拒绝")
	}
	rec := newWFRecorder()
	db := wfOpenRecorderDB(t, rec)
	if _, err := NewPostgresDirectInputReader(db, 30*time.Second, nil); err == nil {
		t.Fatal("TTL 过小必须拒绝")
	}
	if _, err := NewPostgresDirectInputReader(db, 16*time.Minute, nil); err == nil {
		t.Fatal("TTL 过大必须拒绝")
	}
	reader, err := NewPostgresDirectInputReader(db, 5*time.Minute, nil)
	if err != nil {
		t.Fatalf("合法配置必须可用: %v", err)
	}
	if reader.now == nil {
		t.Fatal("now 必须回填默认实现")
	}
}

func TestWFDirectInputCheckContract(t *testing.T) {
	rec := newWFRecorder()
	reader := wfOpenDirectReader(t, rec)
	ctx := context.Background()

	if err := reader.CheckContract(ctx); err != nil {
		t.Fatalf("契约检查必须通过: %v", err)
	}
	joined := ""
	for _, statement := range rec.all() {
		joined += statement.query + "\n"
	}
	for _, required := range []string{
		"SELECT 1 FROM juhe_business.proxy_profiles LIMIT 0",
		"SELECT 1 FROM juhe_business.providers LIMIT 0",
		"SELECT 1 FROM juhe_business.provider_protocol_profiles LIMIT 0",
		"SET LOCAL TRANSACTION READ ONLY",
		"SET LOCAL statement_timeout = '5s'",
		"SET LOCAL lock_timeout = '1s'",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("契约缺少 %q", required)
		}
	}

	// 关系读取失败必须带出关系名。
	relationFailure := errors.New("permission denied")
	rec.failExec("juhe_business.providers", relationFailure)
	err := reader.CheckContract(ctx)
	if err == nil || !strings.Contains(err.Error(), "juhe_business.providers") {
		t.Fatalf("关系失败 err=%v", err)
	}

	// 候选查询失败。
	rec.failQuery("selected_proxies", errors.New("column missing"))
	if err := reader.CheckContract(ctx); err == nil || !strings.Contains(err.Error(), "候选查询失败") {
		t.Fatalf("候选查询失败 err=%v", err)
	}

	// 事务开始失败。
	rec.beginErr = errors.New("too many connections")
	if err := reader.CheckContract(ctx); err == nil || !strings.Contains(err.Error(), "只读事务失败") {
		t.Fatalf("begin 失败 err=%v", err)
	}
	rec.beginErr = nil

	// 提交失败。
	rec.commitErr = errors.New("commit failed")
	if err := reader.CheckContract(ctx); err == nil || !strings.Contains(err.Error(), "契约预检事务失败") {
		t.Fatalf("commit 失败 err=%v", err)
	}

	// 只读事务 SET LOCAL 失败。
	rec2 := newWFRecorder()
	db2 := wfOpenRecorderDB(t, rec2)
	reader2, err := NewPostgresDirectInputReader(db2, 5*time.Minute, func() time.Time { return wfProjBase })
	if err != nil {
		t.Fatalf("构造 reader 失败: %v", err)
	}
	rec2.failExec("SET LOCAL statement_timeout", errors.New("setlocal boom"))
	if err := reader2.CheckContract(ctx); err == nil || !strings.Contains(err.Error(), "只读事务失败") {
		t.Fatalf("SET LOCAL 失败 err=%v", err)
	}

	// 候选游标 Close 失败。
	rec3 := newWFRecorder()
	db3 := wfOpenRecorderDB(t, rec3)
	reader3, err := NewPostgresDirectInputReader(db3, 5*time.Minute, func() time.Time { return wfProjBase })
	if err != nil {
		t.Fatalf("构造 reader 失败: %v", err)
	}
	rec3.scripts = append(rec3.scripts, wfScript{match: "selected_proxies", columns: wfCandidateColumns(), closeErr: errors.New("close boom")})
	if err := reader3.CheckContract(ctx); err == nil || !strings.Contains(err.Error(), "契约游标失败") {
		t.Fatalf("游标关闭失败 err=%v", err)
	}
}

func TestWFDirectInputLoadDueBasics(t *testing.T) {
	rec := newWFRecorder()
	reader := wfOpenDirectReader(t, rec)
	ctx := context.Background()

	if _, err := reader.LoadDue(ctx, 0); err == nil {
		t.Fatal("limit=0 必须拒绝")
	}
	if _, err := reader.LoadDue(ctx, maxProxyLatencyInputLimit+1); err == nil {
		t.Fatal("超上限 limit 必须拒绝")
	}

	// 单页：一个代理两条 target 行聚合成一个 draft。
	rec.script("selected_proxies", wfCandidateColumns(), [][]driver.Value{
		wfCandidateRow("p-1"),
		wfCandidateRow("p-1", func(row []driver.Value) {
			row[9], row[10], row[11] = "gemini", "profile-gemini", "https://gemini.invalid/v1"
		}),
	})
	drafts, err := reader.LoadDue(ctx, 2)
	if err != nil {
		t.Fatalf("LoadDue 失败: %v", err)
	}
	if len(drafts) != 1 {
		t.Fatalf("drafts=%d want 1", len(drafts))
	}
	draft := drafts[0]
	if draft.ProxyID != "p-1" || draft.Trigger != TriggerPeriodic || draft.PolicyVersion != proxyLatencyInputPolicyVersion {
		t.Fatalf("draft 身份=%+v", draft)
	}
	if draft.ConfigRevision != wfProjRevision {
		t.Fatalf("revision=%q", draft.ConfigRevision)
	}
	if !draft.IssuedAt.Equal(wfProjBase.UTC()) || !draft.ExpiresAt.Equal(wfProjBase.Add(5*time.Minute).UTC()) {
		t.Fatalf("签发/过期=%v/%v", draft.IssuedAt, draft.ExpiresAt)
	}
	if draft.ProxyType != "http" || draft.ProxyHost != "10.0.0.1" || draft.ProxyPort != 8080 || draft.ProxyUsername != "user" {
		t.Fatalf("代理字段=%+v", draft)
	}
	if len(draft.Targets) != 2 || draft.Targets[0].Provider != "gpt" || draft.Targets[1].Provider != "gemini" {
		t.Fatalf("targets=%+v", draft.Targets)
	}

	// 查询失败必须传播。
	rec.failQuery("selected_proxies", errors.New("boom"))
	if _, err := reader.LoadDue(ctx, 2); err == nil || !strings.Contains(err.Error(), "候选失败") {
		t.Fatalf("查询失败 err=%v", err)
	}

	// 行迭代中途失败必须传播。
	rec2 := newWFRecorder()
	reader2 := wfOpenDirectReader(t, rec2)
	rec2.scriptRowsErr("selected_proxies", errors.New("broken row"))
	if _, err := reader2.LoadDue(ctx, 2); err == nil || !strings.Contains(err.Error(), "遍历") {
		t.Fatalf("行迭代失败 err=%v", err)
	}
}

func TestWFDirectInputLoadDuePaginationAndSkip(t *testing.T) {
	rec := newWFRecorder()
	reader := wfOpenDirectReader(t, rec)
	ctx := context.Background()

	// limit=1 → pageSize=40。第一页 40 行全部是禁用代理（0 个 draft），
	// 必须翻页；第二页返回 1 个有效代理后终止。
	page1 := make([][]driver.Value, 0, 40)
	for index := 0; index < 40; index++ {
		page1 = append(page1, wfCandidateRow("bad-"+strings.Repeat("x", index+1), func(row []driver.Value) {
			row[6] = false
		}))
	}
	rec.script("selected_proxies", wfCandidateColumns(), page1)
	rec.script("selected_proxies", wfCandidateColumns(), [][]driver.Value{wfCandidateRow("p-good")})
	drafts, err := reader.LoadDue(ctx, 1)
	if err != nil {
		t.Fatalf("LoadDue 失败: %v", err)
	}
	if len(drafts) != 1 || drafts[0].ProxyID != "p-good" {
		t.Fatalf("drafts=%+v 必须只含第二页的有效代理", drafts)
	}
	var queries [][]driver.Value
	for _, statement := range rec.all() {
		if strings.Contains(statement.query, "selected_proxies") {
			queries = append(queries, statement.args)
		}
	}
	if len(queries) != 2 {
		t.Fatalf("查询次数=%d want 2", len(queries))
	}
	if queries[0][0] != int64(40) || queries[0][1] != int64(0) || queries[1][1] != int64(40) {
		t.Fatalf("分页参数=%v", queries)
	}
}

func TestWFDirectInputRowDecoding(t *testing.T) {
	reader := func(t *testing.T) (*wfRecorder, *PostgresDirectInputReader) {
		rec := newWFRecorder()
		return rec, wfOpenDirectReader(t, rec)
	}
	ctx := context.Background()

	// NULL 必填列 → 解码失败。
	rec, readerNull := reader(t)
	rec.script("selected_proxies", wfCandidateColumns(), [][]driver.Value{{
		nil, "http", "10.0.0.1", int64(8080), nil, nil, true, wfProjRevision, nil, "gpt", "profile-gpt", "https://api.openai.com/v1",
	}})
	if _, err := readerNull.LoadDue(ctx, 1); err == nil || !strings.Contains(err.Error(), "列类型无效") {
		t.Fatalf("NULL 必填列 err=%v", err)
	}

	// 列类型不匹配 → 扫描失败。
	rec2, readerBadType := reader(t)
	rec2.script("selected_proxies", wfCandidateColumns(), [][]driver.Value{{
		"p-1", "http", "10.0.0.1", "not-a-port", nil, nil, true, wfProjRevision, nil, "gpt", "profile-gpt", "https://api.openai.com/v1",
	}})
	if _, err := readerBadType.LoadDue(ctx, 1); err == nil || !strings.Contains(err.Error(), "候选失败") {
		t.Fatalf("类型不匹配 err=%v", err)
	}

	// NULL 可空列回退默认值；密码信封合法时进入 draft。
	rec3, readerDefaults := reader(t)
	rec3.script("selected_proxies", wfCandidateColumns(), [][]driver.Value{wfCandidateRow("p-1")})
	drafts, err := readerDefaults.LoadDue(ctx, 1)
	if err != nil || len(drafts) != 1 || drafts[0].ProxyUsername != "user" || drafts[0].ProxyPassword != nil {
		t.Fatalf("默认值 drafts=%+v err=%v", drafts, err)
	}

	// 合法信封 → ProxyPassword 装载。
	rec4, readerEnvelope := reader(t)
	rec4.script("selected_proxies", wfCandidateColumns(), [][]driver.Value{wfCandidateRow("p-1", func(row []driver.Value) {
		row[5] = wfTestEnvelope()
	})})
	drafts, err = readerEnvelope.LoadDue(ctx, 1)
	if err != nil || len(drafts) != 1 || drafts[0].ProxyPassword == nil || drafts[0].ProxyPassword.Ciphertext != wfTestEnvelope() {
		t.Fatalf("信封 drafts=%+v err=%v", drafts, err)
	}
}

func TestWFDirectInputInvalidDraftsSkipped(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		mutate func([]driver.Value)
	}{
		{name: "类型不支持", mutate: func(row []driver.Value) { row[1] = "ftp" }},
		{name: "端口为0", mutate: func(row []driver.Value) { row[3] = int64(0) }},
		{name: "端口超界", mutate: func(row []driver.Value) { row[3] = int64(70000) }},
		{name: "主机空白", mutate: func(row []driver.Value) { row[2] = "  " }},
		{name: "id空白", mutate: func(row []driver.Value) { row[0] = " " }},
		{name: "revision带空白", mutate: func(row []driver.Value) { row[7] = " " + wfProjRevision }},
		{name: "revision非UTC", mutate: func(row []driver.Value) { row[7] = "2026-09-01T11:00:00+08:00" }},
		{name: "lastTested非法", mutate: func(row []driver.Value) { row[8] = "yesterday" }},
		{name: "信封损坏", mutate: func(row []driver.Value) { row[5] = "v1:bad" }},
		{name: "目标标识缺失", mutate: func(row []driver.Value) { row[9], row[10] = "", "" }},
		{name: "provider重复", mutate: func(row []driver.Value) {}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := newWFRecorder()
			reader := wfOpenDirectReader(t, rec)
			rows := [][]driver.Value{wfCandidateRow("p-bad", tt.mutate)}
			if tt.name == "provider重复" {
				rows = [][]driver.Value{
					wfCandidateRow("p-bad"),
					wfCandidateRow("p-bad", func(row []driver.Value) {
						row[9], row[11] = "gpt", "https://other.invalid/v1"
					}),
				}
			}
			rec.script("selected_proxies", wfCandidateColumns(), rows)
			drafts, err := reader.LoadDue(ctx, 5)
			if err != nil {
				t.Fatalf("LoadDue 失败: %v", err)
			}
			for _, draft := range drafts {
				if draft.ProxyID == "p-bad" {
					t.Fatalf("无效代理 %s 不得进入候选：%+v", tt.name, draft)
				}
			}
		})
	}
}

func TestWFDirectInputTargetCanonicalization(t *testing.T) {
	rec := newWFRecorder()
	reader := wfOpenDirectReader(t, rec)
	ctx := context.Background()

	// 非法 URL 目标：保留 provider 身份并持久化稳定失败码，绝不透传原 URL。
	rec.script("selected_proxies", wfCandidateColumns(), [][]driver.Value{wfCandidateRow("p-1", func(row []driver.Value) {
		row[11] = "not a url"
	})})
	drafts, err := reader.LoadDue(ctx, 1)
	if err != nil || len(drafts) != 1 {
		t.Fatalf("drafts=%v err=%v", drafts, err)
	}
	if len(drafts[0].Targets) != 1 || drafts[0].Targets[0].ProbeError != targetProbeErrorInvalidURL || drafts[0].Targets[0].URL != "" {
		t.Fatalf("非法 URL 目标=%+v", drafts[0].Targets)
	}

	// provider 大小写归一。
	rec2 := newWFRecorder()
	reader2 := wfOpenDirectReader(t, rec2)
	rec2.script("selected_proxies", wfCandidateColumns(), [][]driver.Value{wfCandidateRow("p-2", func(row []driver.Value) {
		row[9] = "OpenAI"
	})})
	drafts, err = reader2.LoadDue(ctx, 1)
	if err != nil || len(drafts) != 1 || drafts[0].Targets[0].Provider != "openai" {
		t.Fatalf("provider 归一=%+v err=%v", drafts, err)
	}
}

func TestWFDirectInputPureFuncs(t *testing.T) {
	// parseProxyLatencyUTC。
	if _, err := parseProxyLatencyUTC("2026-09-01T11:00:00Z", "f"); err != nil {
		t.Fatalf("合法 UTC 时间 err=%v", err)
	}
	if _, err := parseProxyLatencyUTC(" 2026-09-01T11:00:00.123456789Z ", "f"); err != nil {
		t.Fatalf("带纳秒合法时间 err=%v", err)
	}
	for _, bad := range []string{"2026-09-01T11:00:00+08:00", "not-a-time", "", "0001-01-01T00:00:00Z"} {
		if _, err := parseProxyLatencyUTC(bad, "f"); err == nil {
			t.Fatalf("%q 必须拒绝", bad)
		}
	}

	// validProxyLatencyEnvelope。
	if !validProxyLatencyEnvelope(wfTestEnvelope()) {
		t.Fatal("合法信封必须通过")
	}
	if validProxyLatencyEnvelope("v1:x:y:z") {
		t.Fatal("段数/长度不足必须拒绝")
	}
	badNonce := "v1:!!!:" + base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef")) + ":" + base64.RawURLEncoding.EncodeToString([]byte("ct"))
	if validProxyLatencyEnvelope(badNonce) {
		t.Fatal("非法 nonce 必须拒绝")
	}
	shortTag := "v1:" + base64.RawURLEncoding.EncodeToString([]byte("123456789012")) + ":" + base64.RawURLEncoding.EncodeToString([]byte("short")) + ":" + base64.RawURLEncoding.EncodeToString([]byte("ct"))
	if validProxyLatencyEnvelope(shortTag) {
		t.Fatal("短 tag 必须拒绝")
	}

	// canonicalizeProbeTarget。
	canonical, err := canonicalizeProbeTarget(Target{Provider: " GPT ", ProfileID: " p ", URL: "https://api.openai.com/v1"})
	if err != nil || canonical.Provider != "gpt" || canonical.ProfileID != "p" {
		t.Fatalf("归一目标=%+v err=%v", canonical, err)
	}
	if _, err := canonicalizeProbeTarget(Target{Provider: "", ProfileID: "p", URL: "https://api.openai.com/v1"}); err == nil {
		t.Fatal("缺 provider 必须拒绝")
	}
	if _, err := canonicalizeProbeTarget(Target{Provider: "gpt", ProfileID: " ", URL: "https://api.openai.com/v1"}); err == nil {
		t.Fatal("缺 profileID 必须拒绝")
	}
	fallback, err := canonicalizeProbeTarget(Target{Provider: "gpt", ProfileID: "p", URL: "::bad"})
	if err != nil || fallback.ProbeError != targetProbeErrorInvalidURL || fallback.URL != "" {
		t.Fatalf("非法 URL 回退=%+v err=%v", fallback, err)
	}
	// 合法失败码且无 URL：保留失败码原样通过（catalog 冻结结果契约）。
	preserved, err := canonicalizeProbeTarget(Target{Provider: "gpt", ProfileID: "p", ProbeError: targetProbeErrorInvalidURL})
	if err != nil || preserved.ProbeError != targetProbeErrorInvalidURL || preserved.URL != "" {
		t.Fatalf("失败码目标=%+v err=%v", preserved, err)
	}
	if _, err := canonicalizeProbeTarget(Target{Provider: "gpt", ProfileID: "p", ProbeError: "other_code"}); err == nil {
		t.Fatal("未知失败码必须拒绝")
	}
	if _, err := canonicalizeProbeTarget(Target{Provider: "gpt", ProfileID: "p", ProbeError: targetProbeErrorInvalidURL, URL: "https://api.openai.com/v1"}); err == nil {
		t.Fatal("失败码与 URL 并存必须拒绝")
	}

	// makeProxyLatencyInputDraft 的 TTL/签发时间守卫。
	assembly := proxyLatencyCandidateAssembly{row: proxyLatencyCandidateRow{
		proxyID: "p-1", proxyType: "http", proxyHost: "10.0.0.1", proxyPort: 8080, proxyEnabled: true,
		configRevision: wfProjRevision, provider: "gpt", profileID: "profile-gpt", targetURL: "https://api.openai.com/v1",
	}, targets: []Target{{Provider: "gpt", ProfileID: "profile-gpt", URL: "https://api.openai.com/v1"}}}
	if _, err := makeProxyLatencyInputDraft(assembly, wfProjBase, 30*time.Second); err == nil {
		t.Fatal("TTL 过小必须拒绝")
	}
	if _, err := makeProxyLatencyInputDraft(assembly, time.Time{}, 5*time.Minute); err == nil {
		t.Fatal("零签发时间必须拒绝")
	}
	draft, err := makeProxyLatencyInputDraft(assembly, wfProjBase, 5*time.Minute)
	if err != nil || draft.ProxyID != "p-1" || len(draft.Targets) != 1 {
		t.Fatalf("合法 draft=%+v err=%v", draft, err)
	}
	if draft.ExpiresAt.Sub(draft.IssuedAt) != 5*time.Minute {
		t.Fatalf("TTL=%v", draft.ExpiresAt.Sub(draft.IssuedAt))
	}
}
