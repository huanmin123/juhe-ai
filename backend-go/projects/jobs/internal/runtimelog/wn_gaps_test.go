package runtimelog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wn_gaps_test.go 覆盖 SQLite store、配置解析、日志行解析、旧 SQLite 迁移
// 与文件清理中的剩余分支。业务契约与既有测试保持一致。

func TestNormalizeLevelCoversNodeLevels(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{raw: `"ERROR"`, want: "error"},
		{raw: `60`, want: "fatal"},
		{raw: `50`, want: "error"},
		{raw: `40`, want: "warn"},
		{raw: `30`, want: "info"},
		{raw: `20`, want: "debug"},
		{raw: `10`, want: "trace"},
		{raw: `5`, want: "trace"},
		{raw: `null`, want: "trace"},
		{raw: `[30]`, want: "info"},
		{raw: ``, want: "info"},
	}
	for _, test := range tests {
		if got := normalizeLevel([]byte(test.raw)); got != test.want {
			t.Fatalf("normalizeLevel(%s) = %q, want %q", test.raw, got, test.want)
		}
	}
}

func TestParseLineFieldFallbacks(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	record := ParseLine(`{"time":"2026-09-01T00:00:00.000Z","level":"INFO","message":"from-message","err":{"message":"err-message"}}`, LineOptions{Now: func() time.Time { return now }})
	if record == nil {
		t.Fatal("JSON 对象行必须生成记录")
	}
	if record.Message != "from-message" {
		t.Fatalf("msg 缺失时必须回退 message 字段: %q", record.Message)
	}
	if record.ErrorMessage != "err-message" {
		t.Fatalf("errorMessage 必须读取 err.message: %q", record.ErrorMessage)
	}

	emptyErr := ParseLine(`{"time":"2026-09-01T00:00:00.000Z","err":{"other":1}}`, LineOptions{Now: func() time.Time { return now }})
	if emptyErr == nil || emptyErr.ErrorMessage != "" {
		t.Fatalf("err 缺少 message 时 errorMessage 必须为空: %#v", emptyErr)
	}
	stringErr := ParseLine(`{"time":"2026-09-01T00:00:00.000Z","err":"plain"}`, LineOptions{Now: func() time.Time { return now }})
	if stringErr == nil || stringErr.ErrorMessage != "" {
		t.Fatalf("err 为字符串时 errorMessage 必须为空: %#v", stringErr)
	}

	// SourceKey 缺省时 stable id 以整行为键；Now 缺省时使用真实时间并保持 RFC3339。
	defaultKey := ParseLine("fallback-line", LineOptions{})
	if defaultKey == nil {
		t.Fatal("回退行必须生成记录")
	}
	if defaultKey.ID != stableID("fallback-line") {
		t.Fatalf("无 SourceKey 时 stable id 必须以整行为键: %s", defaultKey.ID)
	}
	if _, err := normalizeNodeTimestamp(defaultKey.CreatedAt); err != nil {
		t.Fatalf("默认 now 生成的 CreatedAt 必须是合法 RFC3339: %q err=%v", defaultKey.CreatedAt, err)
	}

	if ParseLine("   \t ", LineOptions{}) != nil {
		t.Fatal("空白行不得生成记录")
	}
}

func TestRangeTimesAndFacetRowsFrom(t *testing.T) {
	rows := []facetRow{
		{Time: "2026-08-02T00:00:00.000Z"},
		{Time: "2026-08-01T00:00:00.000Z"},
		{Time: "2026-08-03T00:00:00.000Z"},
	}
	earliest, latest := rangeTimes(rows)
	if earliest != "2026-08-01T00:00:00.000Z" || latest != "2026-08-03T00:00:00.000Z" {
		t.Fatalf("rangeTimes 计算错误: earliest=%q latest=%q", earliest, latest)
	}
	if got := facetRowsFrom(rows, ""); len(got) != 3 {
		t.Fatalf("空 earliest 计数基线必须保留全部行: %d", len(got))
	}
	counted := facetRowsFrom(rows, "2026-08-02T00:00:00.000Z")
	if len(counted) != 2 {
		t.Fatalf("计数基线必须过滤早于基线的行: %#v", counted)
	}
}

func TestFacetEventCountsSkipsBlankEvents(t *testing.T) {
	counts := facetEventCounts([]facetRow{
		{Time: "2026-08-01T00:00:00.000Z", Event: ""},
		{Time: "2026-08-02T00:00:00.000Z", Event: "  "},
		{Time: "2026-08-03T00:00:00.000Z", Event: "login"},
	})
	if len(counts) != 1 {
		t.Fatalf("空白 event 不得计入聚合: %#v", counts)
	}
	if counts["login"].count != 1 || counts["login"].latestTime != "2026-08-03T00:00:00.000Z" {
		t.Fatalf("event 聚合不正确: %#v", counts["login"])
	}
}

func TestConfigValueParsers(t *testing.T) {
	if _, err := parseBool("X", "not-bool", true); err == nil || !strings.Contains(err.Error(), "X") {
		t.Fatalf("非法布尔值必须报错: %v", err)
	}
	if _, err := parseBool("X", "false", true); err != nil {
		t.Fatalf("显式布尔值必须生效: %v", err)
	}
	if _, err := durationOrDefault("X", "0s", time.Second); err == nil {
		t.Fatal("非正 duration 必须报错")
	}
	if _, err := intOrDefault("X", "abc", 3, 1, 10); err == nil {
		t.Fatal("非整数必须报错")
	}
	if value, err := intOrDefault("X", "", 3, 1, 10); err != nil || value != 3 {
		t.Fatalf("缺省值必须生效: %d %v", value, err)
	}
	if _, err := intOrDefault("X", "0", 3, 1, 10); err == nil {
		t.Fatal("低于下界必须报错")
	}
	if _, err := intOrDefault("X", "11", 3, 1, 10); err == nil {
		t.Fatal("高于上界必须报错")
	}
	if _, err := positiveIntOrDefault("X", "-1", 3); err == nil {
		t.Fatal("负连接数必须报错")
	}
	if value, err := positiveIntOrDefault("X", "", 3); err != nil || value != 3 {
		t.Fatalf("连接数缺省值必须生效: %d %v", value, err)
	}
}

// TestLoadConfigModeConstantPostgres 锁定 STORE env 退役后的恒定语义：
// LoadConfig 产出的 Mode 恒 ModePostgres（原 sqlite 缺省/跟随 driver 臂随
// sqlite 形态退役删除）。
func TestLoadConfigModeConstantPostgres(t *testing.T) {
	config, err := LoadConfig(func(name string) string {
		if name == "JUHE_AI_RUNTIME_LOG_POSTGRES_URL" {
			return "postgres://unit-test.invalid/f1"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if config.Mode != ModePostgres {
		t.Fatalf("Mode 恒 postgres: %s", config.Mode)
	}
}

func TestLoadConfigPostgresModeAndRejects(t *testing.T) {
	base := map[string]string{
		"JUHE_AI_RUNTIME_LOG_INSTANCE_ID":  "inst-1",
		"JUHE_AI_RUNTIME_LOG_STORE":        "postgres",
		"JUHE_AI_RUNTIME_LOG_POSTGRES_URL": "postgres://jobs:secret@127.0.0.1:5432/db?sslmode=disable",
		"JUHE_AI_LOG_DIR":                  "logs",
	}
	config, err := LoadConfig(func(name string) string { return base[name] })
	if err != nil {
		t.Fatalf("postgres 模式合法配置必须通过: %v", err)
	}
	if config.Mode != ModePostgres || config.PostgresURL == "" || !config.FileEnabled || config.Once {
		t.Fatalf("postgres 配置解析不正确: %#v", config)
	}
	if config.OwnerID == "" || !strings.HasPrefix(config.OwnerID, "inst-1:") {
		t.Fatalf("OwnerID 必须由实例 ID 与进程号组成: %q", config.OwnerID)
	}

	missingURL := map[string]string{}
	for name, value := range base {
		missingURL[name] = value
	}
	delete(missingURL, "JUHE_AI_RUNTIME_LOG_POSTGRES_URL")
	if _, err := LoadConfig(func(name string) string { return missingURL[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_RUNTIME_LOG_POSTGRES_URL") {
		t.Fatalf("postgres 模式缺少 URL 必须拒绝: %v", err)
	}

	// 2026-09-21 起文件日志恒开（JUHE_AI_LOG_FILE_ENABLED 开关移除），
	// 关闭臂与非法布尔臂随之消失；同配置必须可加载。
	fileAlwaysOn := map[string]string{}
	for name, value := range base {
		fileAlwaysOn[name] = value
	}
	fileAlwaysOn["JUHE_AI_LOG_FILE_ENABLED"] = "false"
	if _, err := LoadConfig(func(name string) string { return fileAlwaysOn[name] }); err != nil {
		t.Fatalf("残留开关值必须被忽略且配置可加载: %v", err)
	}

	onceInvalid := map[string]string{}
	for name, value := range base {
		onceInvalid[name] = value
	}
	onceInvalid["JUHE_AI_RUNTIME_LOG_ONCE"] = "maybe"
	if _, err := LoadConfig(func(name string) string { return onceInvalid[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_RUNTIME_LOG_ONCE") {
		t.Fatalf("非法 ONCE 值必须拒绝: %v", err)
	}

	missingLogDir := map[string]string{}
	for name, value := range base {
		missingLogDir[name] = value
	}
	delete(missingLogDir, "JUHE_AI_LOG_DIR")
	// 缺省 LOG_DIR 改为派生 <DATA_DIR>/logs 并代建目录（2026-09-19 零配置
	// 决策）；DATA_DIR 指向临时目录，验证派生与创建且不污染包目录。
	dataRoot := t.TempDir()
	missingLogDir["JUHE_AI_DATA_DIR"] = dataRoot
	derivedConfig, deriveErr := LoadConfig(func(name string) string { return missingLogDir[name] })
	if deriveErr != nil {
		t.Fatalf("缺省 LOG_DIR 必须从 DATA_DIR 派生: %v", deriveErr)
	}
	expectedLogDir := filepath.Join(dataRoot, "logs")
	if derivedConfig.LogDirectory != expectedLogDir {
		t.Fatalf("LOG_DIR 必须派生为 <DATA_DIR>/logs: %q", derivedConfig.LogDirectory)
	}
	if _, statErr := os.Stat(expectedLogDir); statErr != nil {
		t.Fatalf("派生日志目录必须被创建: %v", statErr)
	}

	badLease := map[string]string{}
	for name, value := range base {
		badLease[name] = value
	}
	badLease["JUHE_AI_RUNTIME_LOG_OWNER_LEASE"] = "-1s"
	if _, err := LoadConfig(func(name string) string { return badLease[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_RUNTIME_LOG_OWNER_LEASE") {
		t.Fatalf("非正 owner lease 必须拒绝: %v", err)
	}

	tooManyMinConns := map[string]string{}
	for name, value := range base {
		tooManyMinConns[name] = value
	}
	tooManyMinConns["JUHE_AI_RUNTIME_LOG_POSTGRES_MIN_CONNS"] = "11"
	if _, err := LoadConfig(func(name string) string { return tooManyMinConns[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_RUNTIME_LOG_POSTGRES_MIN_CONNS") {
		t.Fatalf("最小连接数超过平台上限必须拒绝: %v", err)
	}

	outOfRangeRetention := map[string]string{}
	for name, value := range base {
		outOfRangeRetention[name] = value
	}
	outOfRangeRetention["JUHE_AI_RUNTIME_LOG_RETENTION_DAYS"] = "91"
	if _, err := LoadConfig(func(name string) string { return outOfRangeRetention[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_RUNTIME_LOG_RETENTION_DAYS") {
		t.Fatalf("保留天数越界必须拒绝: %v", err)
	}
}

func TestOwnerLeaseContextHelpers(t *testing.T) {
	if _, err := ownerLeaseFromContext(context.Background()); err == nil {
		t.Fatal("无 lease 上下文必须报错")
	}
	lease, err := ownerLeaseFromContext(withOwnerLease(context.Background(), OwnerLease{OwnerID: "owner", FenceToken: 3}))
	if err != nil || lease.FenceToken != 3 {
		t.Fatalf("lease 上下文必须可还原: %#v err=%v", lease, err)
	}
	if _, err := ownerLeaseFromContext(withOwnerLease(context.Background(), OwnerLease{FenceToken: 3})); err == nil {
		t.Fatal("缺少 owner 的 lease 必须报错")
	}
	if _, err := ownerLeaseFromContext(withOwnerLease(context.Background(), OwnerLease{OwnerID: "owner"})); err == nil {
		t.Fatal("缺少 fence token 的 lease 必须报错")
	}
}

type stubLeaseStore struct {
	Store
	acquireErr    error
	acquireLost   bool
	releasePanics bool
}

func (store *stubLeaseStore) AcquireOwnerLease(context.Context, string, time.Duration) (OwnerLease, bool, error) {
	if store.acquireErr != nil {
		return OwnerLease{}, false, store.acquireErr
	}
	return OwnerLease{}, !store.acquireLost, nil
}

func (store *stubLeaseStore) ReleaseOwnerLease(context.Context, OwnerLease) error {
	if store.releasePanics {
		panic("release lease fixture panic")
	}
	return nil
}

func TestRunWithOwnerLeaseReportsAcquireFailures(t *testing.T) {
	config := Config{OwnerID: "stub-owner", OwnerLease: time.Minute}
	run := func(context.Context) error { return nil }

	err := RunWithOwnerLease(context.Background(), config, &stubLeaseStore{acquireErr: errors.New("acquire fixture failure")}, run)
	if err == nil || !strings.Contains(err.Error(), "acquire fixture failure") {
		t.Fatalf("获取 lease 失败必须返回错误: %v", err)
	}
	err = RunWithOwnerLease(context.Background(), config, &stubLeaseStore{acquireLost: true}, run)
	if err == nil || !strings.Contains(err.Error(), "已由另一个 Go 实例持有") {
		t.Fatalf("lease 被占用必须返回错误: %v", err)
	}
	err = RunWithOwnerLease(context.Background(), config, &stubLeaseStore{releasePanics: true}, run)
	if err == nil || !strings.Contains(err.Error(), "释放运行日志 owner lease panic") {
		t.Fatalf("释放 panic 必须转换为错误: %v", err)
	}
}

