package auditlog

// F3 审计 retention 的业务库设置热读（复用 F4 operationlog
// store.RetentionDays 先例：每 tick 直读 system_settings、无缓存、读失败
// 跳过本轮不扩大清理范围）。连接懒打开并按 reader 实例复用（单句柄）：
// owner 的 reader 随 maintenance 代际关闭；手动清理路由的 reader 由组合根
// 持有、进程生命周期使用（停机随进程释放，镜像只读句柄的既有模式）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// postgresSettingsApplicationName 区分该只读句柄的业务库访问来源。
const postgresSettingsApplicationName = "juhe-ai-gateway-f3-audit-settings"

// system_settings 中的四个审计保留键（settings 包 log-retention 分区同款键名）。
const (
	settingsKeySuccessRetentionDays     = "auditLogSuccessRetentionDays"
	settingsKeyProblemRetentionDays     = "auditLogProblemRetentionDays"
	settingsKeySuccessHotRetentionHours = "auditLogSuccessHotRetentionHours"
	settingsKeySuccessSampleRate        = "auditLogSuccessSampleRate"
)

// RetentionSettings 是一轮业务库热读的结果：四个审计保留值 + 每键是否存在
// DB 行（缺行回落 env 固化值，见 Config.EffectiveRetentionSettings）。
type RetentionSettings struct {
	SuccessRetentionDays     int
	ProblemRetentionDays     int
	SuccessHotRetentionHours int
	SuccessSampleRate        float64

	HasSuccessRetentionDays     bool
	HasProblemRetentionDays     bool
	HasSuccessHotRetentionHours bool
	HasSuccessSampleRate        bool
}

// EffectiveRetentionSettings 按生效优先级链合成下一轮 retention 使用的配置：
// DB 行存在用 DB 值，否则用 env 固化值（LoadConfig 未显式配置时即代码默认）。
// 其余字段（批量大小等）保持 env 固化值不变。
func (cfg Config) EffectiveRetentionSettings(settings RetentionSettings) Config {
	effective := cfg
	if settings.HasSuccessRetentionDays {
		effective.SuccessRetentionDays = settings.SuccessRetentionDays
	}
	if settings.HasProblemRetentionDays {
		effective.ProblemRetentionDays = settings.ProblemRetentionDays
	}
	if settings.HasSuccessHotRetentionHours {
		effective.SuccessHotRetentionHours = settings.SuccessHotRetentionHours
	}
	if settings.HasSuccessSampleRate {
		effective.SuccessSampleRate = settings.SuccessSampleRate
	}
	return effective
}

// BusinessSettingsReader 是业务库 system_settings 的只读懒加载 reader：首读
// 打开单句柄并复用；并发安全（owner tick 与手动清理路由可各持一个实例）。
type BusinessSettingsReader struct {
	mode        Mode
	sqlitePath  string
	postgresURL string

	mu sync.Mutex
	db *sql.DB
}

// NewBusinessSettingsReader 按 Config 的业务设置目标构建 reader：postgres
// 模式用 BusinessSettingsURL（juhe_business schema 限定），sqlite 模式用
// BusinessSettingsPath（mode=ro + query_only 只读句柄）。两者都为空时
// ReadRetentionSettings 返回全缺省（无 DB 覆盖，仅 env 生效）——生产
// LoadConfig 保证二选一非空，空目标只出现在未走 LoadConfig 的直接构造
// （单测 fake）场景。
func NewBusinessSettingsReader(cfg Config) *BusinessSettingsReader {
	if cfg.Mode == ModePostgres {
		return &BusinessSettingsReader{mode: ModePostgres, postgresURL: strings.TrimSpace(cfg.BusinessSettingsURL)}
	}
	return &BusinessSettingsReader{mode: ModeSQLite, sqlitePath: strings.TrimSpace(cfg.BusinessSettingsPath)}
}

// Close 关闭懒打开的句柄；幂等。owner 侧随 maintenance 代际退出调用。
func (r *BusinessSettingsReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.db == nil {
		return nil
	}
	err := r.db.Close()
	r.db = nil
	return err
}

// configured 报告 reader 是否装配了业务设置读取目标。
func (r *BusinessSettingsReader) configured() bool {
	if r.mode == ModePostgres {
		return r.postgresURL != ""
	}
	return r.sqlitePath != ""
}

// database 懒打开并复用只读句柄（进程内每 reader 单句柄）。
func (r *BusinessSettingsReader) database() (*sql.DB, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.db != nil {
		return r.db, nil
	}
	if r.mode == ModePostgres {
		if r.postgresURL == "" {
			return nil, fmt.Errorf("F3 审计保留设置未装配业务库读取目标（postgres URL 为空）")
		}
		pgConfig, err := pgx.ParseConfig(r.postgresURL)
		if err != nil {
			return nil, fmt.Errorf("解析 F3 审计保留设置 PostgreSQL URL 失败: %w", err)
		}
		if pgConfig.RuntimeParams == nil {
			pgConfig.RuntimeParams = map[string]string{}
		}
		pgConfig.RuntimeParams["application_name"] = postgresSettingsApplicationName
		db := stdlib.OpenDB(*pgConfig)
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		r.db = db
		return db, nil
	}
	if r.sqlitePath == "" {
		return nil, fmt.Errorf("F3 审计保留设置未装配业务库读取目标（sqlite 路径为空）")
	}
	db, err := openSettingsSQLiteReadOnly(r.sqlitePath)
	if err != nil {
		return nil, err
	}
	r.db = db
	return db, nil
}

// openSettingsSQLiteReadOnly 以 mode=ro + query_only 打开业务库只读句柄
// （F4 operationlog openSQLiteReadOnly 同款双保险）。
func openSettingsSQLiteReadOnly(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("读取 F3 审计保留设置业务库失败: %w", err)
	}
	filePath := filepath.ToSlash(abs)
	if !strings.HasPrefix(filePath, "/") {
		filePath = "/" + filePath
	}
	dsn := (&url.URL{Scheme: "file", Path: filePath, RawQuery: "mode=ro&_pragma=query_only(1)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec("PRAGMA query_only=ON"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// ReadRetentionSettings 直读四个审计保留键。未装配读取目标时返回全缺省
//（无覆盖，仅 env 生效）；行存在但值非法按 F4 语义报错（调用方跳过本轮，
// 不静默扩大或缩小清理范围）。
func (r *BusinessSettingsReader) ReadRetentionSettings(ctx context.Context) (RetentionSettings, error) {
	if !r.configured() {
		return RetentionSettings{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := r.database()
	if err != nil {
		return RetentionSettings{}, err
	}
	query := `SELECT key, value_json FROM system_settings WHERE system_account_id = ? AND key IN (?,?,?,?)`
	if r.mode == ModePostgres {
		query = `SELECT key, value_json FROM juhe_business.system_settings WHERE system_account_id = $1 AND key IN ($2,$3,$4,$5)`
	}
	args := []any{"sys_admin", settingsKeySuccessRetentionDays, settingsKeyProblemRetentionDays, settingsKeySuccessHotRetentionHours, settingsKeySuccessSampleRate}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return RetentionSettings{}, fmt.Errorf("读取 F3 审计保留设置失败: %w", err)
	}
	defer rows.Close()
	found := map[string]float64{}
	for rows.Next() {
		var key, valueJSON string
		if err := rows.Scan(&key, &valueJSON); err != nil {
			return RetentionSettings{}, fmt.Errorf("读取 F3 审计保留设置行失败: %w", err)
		}
		var decoded any
		if err := json.Unmarshal([]byte(valueJSON), &decoded); err != nil {
			return RetentionSettings{}, fmt.Errorf("F3 审计保留设置键 %s 不是合法 JSON: %w", key, err)
		}
		number, ok := decoded.(float64)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
			return RetentionSettings{}, fmt.Errorf("F3 审计保留设置键 %s 必须是数字", key)
		}
		found[key] = number
	}
	if err := rows.Err(); err != nil {
		return RetentionSettings{}, fmt.Errorf("遍历 F3 审计保留设置失败: %w", err)
	}
	settings := RetentionSettings{}
	if number, ok := found[settingsKeySuccessRetentionDays]; ok {
		if number != math.Trunc(number) || number < 0 || number > 3650 {
			return RetentionSettings{}, fmt.Errorf("%s 必须是 0 到 3650 之间的整数", settingsKeySuccessRetentionDays)
		}
		settings.SuccessRetentionDays, settings.HasSuccessRetentionDays = int(number), true
	}
	if number, ok := found[settingsKeyProblemRetentionDays]; ok {
		if number != math.Trunc(number) || number < 1 || number > 3650 {
			return RetentionSettings{}, fmt.Errorf("%s 必须是 1 到 3650 之间的整数", settingsKeyProblemRetentionDays)
		}
		settings.ProblemRetentionDays, settings.HasProblemRetentionDays = int(number), true
	}
	if number, ok := found[settingsKeySuccessHotRetentionHours]; ok {
		if number != math.Trunc(number) || number < 0 || number > 168 {
			return RetentionSettings{}, fmt.Errorf("%s 必须是 0 到 168 之间的整数", settingsKeySuccessHotRetentionHours)
		}
		settings.SuccessHotRetentionHours, settings.HasSuccessHotRetentionHours = int(number), true
	}
	if number, ok := found[settingsKeySuccessSampleRate]; ok {
		if number < 0 || number > 1 || settingsDecimalPlaces(number) > 4 {
			return RetentionSettings{}, fmt.Errorf("%s 必须是 0 到 1 之间且最多 4 位小数的数字", settingsKeySuccessSampleRate)
		}
		settings.SuccessSampleRate, settings.HasSuccessSampleRate = number, true
	}
	return settings, nil
}

// settingsDecimalPlaces 用最短十进制文本统计小数位数（与 settings 包
// normalizeSuccessSampleRateSetting 同口径）。
func settingsDecimalPlaces(number float64) int {
	text := strconv.FormatFloat(number, 'f', -1, 64)
	if point := strings.IndexByte(text, '.'); point >= 0 {
		return len(text) - point - 1
	}
	return 0
}
