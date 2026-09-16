package logreads

// w9b 纯 helper 与错误臂补充：方言函数、mapper 类型矩阵、NDJSON 行读取、
// grep 排序/规整、构造器守卫。全部不触真实数据集。

import (
	"bufio"
	"database/sql"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/operationlog"
)

func TestW9BBindPostgresOrdinals(t *testing.T) {
	if got := ReadPostgres.bind("a = ? AND b = ? AND c=?"); got != "a = $1 AND b = $2 AND c=$3" {
		t.Fatalf("bind=%q", got)
	}
	if got := ReadSQLite.bind("a = ?"); got != "a = ?" {
		t.Fatalf("sqlite bind=%q", got)
	}
}

func TestW9BTimeParamModes(t *testing.T) {
	if got := ReadSQLite.timeParam("2026-09-01T00:00:00.000Z"); got != "2026-09-01T00:00:00.000Z" {
		t.Fatalf("sqlite timeParam=%v", got)
	}
	parsed, ok := ReadPostgres.timeParam("2026-09-01T00:00:00.000Z").(time.Time)
	if !ok || parsed.UTC().Format(time.RFC3339) != "2026-09-01T00:00:00Z" {
		t.Fatalf("postgres timeParam=%v", ReadPostgres.timeParam("2026-09-01T00:00:00.000Z"))
	}
	// 非 RFC3339 回退字符串。
	if got := ReadPostgres.timeParam("not-a-time"); got != "not-a-time" {
		t.Fatalf("fallback=%v", got)
	}
}

func TestW9BParseReadDBModeArms(t *testing.T) {
	if _, err := parseReadDBMode(""); err == nil || !strings.Contains(err.Error(), "必填") {
		t.Fatalf("空模式=%v", err)
	}
	if _, err := parseReadDBMode("mysql"); err == nil || !strings.Contains(err.Error(), "mysql") {
		t.Fatalf("非法模式=%v", err)
	}
	if mode, err := parseReadDBMode(ReadPostgres); err != nil || mode != ReadPostgres {
		t.Fatalf("postgres=%v err=%v", mode, err)
	}
}

func TestW9BReadRawStringMatrix(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{nil, ""},
		{"text", "text"},
		{[]byte("bytes"), "bytes"},
		{time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), "2026-09-01T00:00:00.000Z"},
		{int64(42), "42"},
		{float64(1.5), "1.5"},
		{true, "true"},
		{false, "false"},
		{struct{}{}, "{}"},
	}
	for _, tc := range cases {
		if got := readRawString(tc.value); got != tc.want {
			t.Fatalf("readRawString(%#v)=%q want %q", tc.value, got, tc.want)
		}
	}
}

func TestW9BReadInt64Matrix(t *testing.T) {
	cases := []struct {
		value any
		want  int64
		ok    bool
	}{
		{nil, 0, false},
		{int64(7), 7, true},
		{int(9), 9, true},
		{float64(2.9), 2, true},
		{true, 1, true},
		{false, 0, true},
		{" 12 ", 12, true},
		{"bad", 0, false},
		{time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), true},
		{struct{}{}, 0, false},
	}
	for _, tc := range cases {
		got, ok := readInt64(tc.value)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Fatalf("readInt64(%#v)=%v,%v want %v,%v", tc.value, got, ok, tc.want, tc.ok)
		}
	}
	// NaN/Inf 拒绝。
	if _, ok := readInt64(float64(math.NaN())); ok {
		t.Fatal("NaN 必须拒绝")
	}
	if _, ok := readInt64("nan"); ok {
		t.Fatal("字符串 NaN 必须拒绝")
	}
}

func TestW9BReadBoolMatrix(t *testing.T) {
	cases := []struct {
		value any
		want  bool
	}{
		{nil, false},
		{true, true},
		{false, false},
		{int64(2), true},
		{int64(0), false},
		{int(1), true},
		{float64(0), false},
		{float64(1.5), true},
		{"1", true},
		{"TRUE", true},
		{"no", false},
		{struct{}{}, false},
	}
	for _, tc := range cases {
		if got := readBool(tc.value); got != tc.want {
			t.Fatalf("readBool(%#v)=%v want %v", tc.value, got, tc.want)
		}
	}
}

func TestW9BParseInstantMillisVariants(t *testing.T) {
	if _, err := parseHotInstantMillis("2026-09-01T00:00:00Z"); err != nil {
		t.Fatalf("合法时间=%v", err)
	}
	if _, err := parseHotInstantMillis("nope"); err == nil || !strings.Contains(err.Error(), "RFC3339") {
		t.Fatalf("非法时间=%v", err)
	}
	if _, err := parseGrepInstantMillis("2026-09-01T00:00:00Z"); err != nil {
		t.Fatalf("grep 合法=%v", err)
	}
	if _, err := parseGrepInstantMillis("nope"); err == nil || !strings.Contains(err.Error(), "运行日志") {
		t.Fatalf("grep 非法=%v", err)
	}
}

func TestW9BReadBoundedHotLineArms(t *testing.T) {
	// 正常行。
	reader := bufio.NewReader(strings.NewReader("alpha\nbeta"))
	raw, consumed, cleanEOF, tooLong, err := readBoundedHotLine(reader, 100)
	if err != nil || tooLong || cleanEOF || string(raw) != "alpha" || consumed != 6 {
		t.Fatalf("正常行=%q consumed=%d eof=%v tooLong=%v err=%v", raw, consumed, cleanEOF, tooLong, err)
	}
	// 第二行（无换行尾行）+ 随后的干净 EOF。
	raw, consumed, cleanEOF, tooLong, err = readBoundedHotLine(reader, 100)
	if err != nil || !cleanEOF || tooLong || string(raw) != "beta" || consumed != 4 {
		t.Fatalf("第二行=%q eof=%v err=%v", raw, cleanEOF, err)
	}
	raw, consumed, cleanEOF, tooLong, err = readBoundedHotLine(reader, 100)
	if err != nil || !cleanEOF || tooLong || raw != nil || consumed != 0 {
		t.Fatalf("EOF=%q eof=%v err=%v", raw, cleanEOF, err)
	}
	// EOF 前的尾行（无换行）。
	reader = bufio.NewReader(strings.NewReader("tail"))
	raw, _, cleanEOF, tooLong, err = readBoundedHotLine(reader, 100)
	if err != nil || !cleanEOF || tooLong || string(raw) != "tail" {
		t.Fatalf("尾行=%q eof=%v tooLong=%v err=%v", raw, cleanEOF, tooLong, err)
	}
	// 超长行（单缓冲内）。
	reader = bufio.NewReader(strings.NewReader(strings.Repeat("x", 300) + "\n"))
	_, _, _, tooLong, err = readBoundedHotLine(reader, 100)
	if err != nil || !tooLong {
		t.Fatalf("超长行 tooLong=%v err=%v", tooLong, err)
	}
	// ErrBufferFull 驱动的超长行（跨缓冲 drain）。
	reader = bufio.NewReaderSize(strings.NewReader(strings.Repeat("y", 5000)+"\n"), 64)
	_, _, _, tooLong, err = readBoundedHotLine(reader, 100)
	if err != nil || !tooLong {
		t.Fatalf("跨缓冲超长 tooLong=%v err=%v", tooLong, err)
	}
	// drainRestOfHotLine：EOF 终止的超长尾行。
	reader = bufio.NewReaderSize(strings.NewReader(strings.Repeat("z", 5000)), 64)
	if err := drainRestOfHotLine(reader); err != nil {
		t.Fatalf("drain=%v", err)
	}
}

func TestW9BCompareAndInsertGrepItems(t *testing.T) {
	older := orderedGrepItem{item: RuntimeLogGrepItem{LineNumber: 5}, fileOrder: 0, sortTimeMs: 100}
	newer := orderedGrepItem{item: RuntimeLogGrepItem{LineNumber: 1}, fileOrder: 1, sortTimeMs: 200}
	if !compareGrepItems(newer, older) {
		t.Fatal("新时间必须在前")
	}
	if compareGrepItems(older, newer) {
		t.Fatal("比较必须非对称一致")
	}
	sameTime := orderedGrepItem{item: RuntimeLogGrepItem{LineNumber: 3}, fileOrder: 0, sortTimeMs: 100}
	if !compareGrepItems(older, sameTime) {
		t.Fatal("同时间按文件序")
	}
	sameFile := orderedGrepItem{item: RuntimeLogGrepItem{LineNumber: 9}, fileOrder: 0, sortTimeMs: 100}
	if compareGrepItems(older, sameFile) {
		t.Fatal("同文件行号大者在前")
	}
	// 插入排序限制条数。
	items := []orderedGrepItem{}
	for index := 0; index < 5; index++ {
		items = insertLatestGrepItem(items, orderedGrepItem{item: RuntimeLogGrepItem{LineNumber: int64(index)}, sortTimeMs: int64(index)}, 3)
	}
	if len(items) != 3 || items[0].item.LineNumber != 4 {
		t.Fatalf("插入排序=%d 首=%d", len(items), items[0].item.LineNumber)
	}
}

func TestW9BGrepNormalizeLevelMatrix(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{"ERROR", "error"},
		{"  ", "info"},
		{nil, "info"},
		{float64(60), "fatal"},
		{float64(50), "error"},
		{float64(40), "warn"},
		{float64(30), "info"},
		{float64(20), "debug"},
		{float64(10), "trace"},
		{struct{}{}, "info"},
	}
	for _, tc := range cases {
		if got := grepNormalizeLevel(tc.value); got != tc.want {
			t.Fatalf("grepNormalizeLevel(%#v)=%q want %q", tc.value, got, tc.want)
		}
	}
}

func TestW9BGrepTimeAndTrimHelpers(t *testing.T) {
	if got := grepTimeValue(" 2026-09-01T00:00:00Z "); got != "2026-09-01T00:00:00Z" {
		t.Fatalf("string time=%q", got)
	}
	if got := grepTimeValue(float64(0)); got == "" {
		t.Fatalf("毫秒时间=%q", got)
	}
	if got := grepTimeValue(nil); got != "" {
		t.Fatalf("空值=%q", got)
	}
	if got := trimGrepText(strings.Repeat("字", 30), 10); len([]rune(got)) != 13 || !strings.HasSuffix(got, "...") {
		t.Fatalf("trim=%q", got)
	}
	if got := trimGrepText("short", 10); got != "short" {
		t.Fatalf("未截断=%q", got)
	}
}

func TestW9BEmptyAuditBlobWindowArms(t *testing.T) {
	full := emptyAuditBlobWindow(true, 5, 100, 42, "full")
	if full.offset != 0 || full.limit != 100 || full.totalBytes != 42 || full.status != "full" {
		t.Fatalf("full=%+v", full)
	}
	bounded := emptyAuditBlobWindow(false, 7, 100, 42, "bounded")
	if bounded.offset != 7 || bounded.limit != 100 || bounded.status != "bounded" {
		t.Fatalf("bounded=%+v", bounded)
	}
}

func TestW9BReaderConstructors(t *testing.T) {
	if _, err := NewAuditLogSQLReader(nil, ReadSQLite); err == nil {
		t.Fatal("nil db 必须拒绝")
	}
	if _, err := NewAuditLogSQLReader(&sql.DB{}, ""); err == nil {
		t.Fatal("空模式必须拒绝")
	}
	reader, err := NewAuditLogSQLReader(&sql.DB{}, ReadPostgres)
	if err != nil {
		t.Fatalf("postgres reader=%v", err)
	}
	if reader == nil {
		t.Fatal("reader 不能为 nil")
	}
	if _, err := NewRuntimeLogSQLReaderWithSources(nil, ReadSQLite, nil, nil); err == nil {
		t.Fatal("nil db 必须拒绝")
	}
	withSources, err := NewRuntimeLogSQLReaderWithSources(&sql.DB{}, ReadSQLite, func() int { return 30 }, func() time.Time { return time.Unix(0, 0) })
	if err != nil || withSources == nil {
		t.Fatalf("WithSources=%v err=%v", withSources, err)
	}
}

func TestW9BWriteReadErrorArms(t *testing.T) {
	deps := &Deps{}
	// 普通错误 → 500。
	recorder := httptest.NewRecorder()
	deps.writeReadError(recorder, errors.New("boom"))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("普通错误=%d", recorder.Code)
	}
	// ErrInvalidListTime → 400。
	recorder = httptest.NewRecorder()
	deps.writeReadError(recorder, operationlog.ErrInvalidListTime)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("时间错误=%d", recorder.Code)
	}
}

func TestW9BAppendAuditPathFilterArms(t *testing.T) {
	clauses := []string{}
	params := []any{}
	appendAuditPathFilter(&clauses, &params, "path", "/api/accounts")
	if len(clauses) != 1 || len(params) != 1 {
		t.Fatalf("路径过滤=%v %v", clauses, params)
	}
	// 空值跳过。
	appendAuditPathFilter(&clauses, &params, "path", "  ")
	if len(clauses) != 1 {
		t.Fatalf("空值不应追加=%v", clauses)
	}
}

func TestW9BReadQueryHelpers(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/x?page=3&pageSize=abc&status=250&status2=99&startAt=2026-09-01T00:00:00Z", nil)
	if value := readQueryInt(request, "page"); value == nil || *value != 3 {
		t.Fatalf("page=%v", value)
	}
	if value := readQueryInt(request, "pageSize"); value != nil {
		t.Fatalf("非法数字=%v", value)
	}
	if value := readQueryStatusCode(request, "status"); value == nil || *value != 250 {
		t.Fatalf("status=%v", value)
	}
	// status2=99 不是合法 HTTP 状态码 → nil。
	if value := readQueryStatusCode(request, "status2"); value != nil {
		t.Fatalf("非法状态码=%v", value)
	}
	if text, ok := readCanonicalInstant("2026-09-01T08:00:00+08:00"); !ok || text != "2026-09-01T00:00:00.000Z" {
		t.Fatalf("canonical=%q ok=%v", text, ok)
	}
	if _, ok := readCanonicalInstant("nope"); ok {
		t.Fatal("非法 instant 必须失败")
	}
}

func TestW9BReadJSONObjectArms(t *testing.T) {
	valid := readJSONObject([]byte(`{"a":1}`))
	if valid == nil || valid["a"] == nil {
		t.Fatalf("合法对象=%v", valid)
	}
	if got := readJSONObject([]byte(`[1,2]`)); len(got) != 0 {
		t.Fatal("数组必须返回空对象")
	}
	if got := readJSONObject([]byte(`{invalid`)); len(got) != 0 {
		t.Fatal("坏 JSON 必须返回空对象")
	}
	if got := readJSONObject(nil); len(got) != 0 {
		t.Fatal("nil 必须返回空对象")
	}
}
