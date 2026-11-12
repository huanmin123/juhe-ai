// Package dbtime 是数据库绝对时刻的唯一编解码层。
//
// 两类语义必须分开：
//   - API 契约：UTC、毫秒、固定宽度（YYYY-MM-DDTHH:MM:SS.sssZ）；
//   - PostgreSQL timestamptz：UTC、微秒精度（必要时毫秒契约列显式截断）。
//
// 本包只处理“瞬时”，不处理日期键/业务日历键；后者见 statcalendar。
// 迁移完成前，text 时间列仍必须写 timefmt.FormatRFC3339Millis 的定宽毫秒；
// DBText 只允许用于已经是 timestamptz 的列，避免把微秒文本写进 text 列破坏
// 字典序。
package dbtime

import (
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/timefmt"
)

// Precision 是绝对时刻的输出/绑定精度。
type Precision int

const (
	// Millis 是 Node toISOString / API 契约的毫秒精度。
	Millis Precision = iota
	// Micros 是 PostgreSQL timestamptz 的存储精度。
	Micros
)

const (
	layoutMillis = "2006-01-02T15:04:05.000Z"
	layoutMicros = "2006-01-02T15:04:05.000000Z"

	sqlNowMillis  = `to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')`
	sqlNowMicros  = `to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')`
	sqlTextLayout = `'YYYY-MM-DD"T"HH24:MI:SS.`
)

// Layout 返回指定精度的 Go 时间布局。
func Layout(p Precision) string {
	if p == Micros {
		return layoutMicros
	}
	return layoutMillis
}

// Format 以 UTC 固定宽度输出。
func Format(t time.Time, p Precision) string {
	return t.UTC().Format(Layout(p))
}

// API 输出 API 契约的毫秒 UTC `Z` 格式。
func API(t time.Time) string {
	return Format(t, Millis)
}

// API6 输出已有微秒契约字段的 UTC `Z` 格式；新字段默认不要使用。
func API6(t time.Time) string {
	return Format(t, Micros)
}

// DBText 输出 UTC 微秒文本；仅用于已是 timestamptz 的列或迁移诊断，
// 不得写入 text 时间列。
func DBText(t time.Time) string {
	return Format(t, Micros)
}

// ParseAPI 解析 API 输入的绝对时刻，必须带 Z 或数值 offset；小数超过
// 3 位按毫秒截断（与 timefmt/Node 语义一致）。
func ParseAPI(value string) (time.Time, bool) {
	return timefmt.ParseRFC3339Instant(value)
}

// ParseAPIRequired 解析 API 输入，非法时返回带 label 的错误。
func ParseAPIRequired(value, label string) (time.Time, error) {
	parsed, ok := ParseAPI(value)
	if !ok {
		return time.Time{}, fmt.Errorf("%s必须是带 Z 或数值 offset 的 RFC3339 时间", label)
	}
	return parsed.UTC(), nil
}

// ParseDB 解析数据库/RFC3339Nano 输入并归一 UTC；解析保留纳秒，数据库
// timestamptz 源最多微秒，因此不会丢精度。无 offset、空格分隔、前后空白、
// 非法日历日期一律拒绝；需要容忍存储侧空白的调用方应使用 Nullable.Scan。
// Go 的 time.Parse 接受 RFC3339 允许的小写 t/z，这里不额外收紧；API 入口
// 由 timefmt.ParseRFC3339Instant 保持大写契约。
func ParseDB(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

// ParseDBRequired 解析数据库输入，非法时返回带 label 的错误。
func ParseDBRequired(value, label string) (time.Time, error) {
	parsed, ok := ParseDB(value)
	if !ok {
		return time.Time{}, fmt.Errorf("%s 必须是带 Z 或数值 offset 的 RFC3339 时间", label)
	}
	return parsed, nil
}

// DBValue 返回可直接绑定 timestamptz 的 time.Time。显式截断到微秒，
// 避免 pgx 二进制编码与 PostgreSQL 文本输入在纳秒尾上的 1µs 舍入差。
func DBValue(t time.Time) time.Time {
	return t.UTC().Truncate(time.Microsecond)
}

// DBValueMillis 返回毫秒精度的绑定值，用于 timestamptz(3)/毫秒等值契约列
// （例如 usage_records.created_at）。
func DBValueMillis(t time.Time) time.Time {
	return t.UTC().Truncate(time.Millisecond)
}

// DBValueOrNil 绑定可空 timestamptz。
func DBValueOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return DBValue(*t)
}

// DBValueMillisOrNil 绑定可空毫秒契约 timestamptz。
func DBValueMillisOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return DBValueMillis(*t)
}

// DBArg 把 API/DB 时间文本转换为 timestamptz 列的绑定参数。无法解析时保留
// 原字符串，让 PostgreSQL 以类型错误显式失败，而不是静默写入错误时间。
func DBArg(value string) any {
	parsed, ok := ParseDB(value)
	if !ok {
		return value
	}
	return DBValueMillis(parsed)
}

// SameInstant 报告两个时间文本是否表示同一毫秒时刻。用于乐观锁入参：数据库
// 读出值已归一为定宽毫秒/微秒，而客户端可能仍持有迁移前的变宽文本；无法解析
// 的一侧退化为字符串相等，保证非法 revision 仍然冲突。
func SameInstant(a, b string) bool {
	left, right := strings.TrimSpace(a), strings.TrimSpace(b)
	ta, oka := ParseDB(left)
	tb, okb := ParseDB(right)
	if !oka || !okb {
		return left == right
	}
	return ta.UnixMilli() == tb.UnixMilli()
}

// Nullable 是迁移期/维护命令使用的可空时间扫描器：同时接受
// nil/string/[]byte/time.Time，并统一归一 UTC。运行路径在类型切换完成后
// 应只接受 time.Time，不保留旧列回落。
type Nullable struct {
	Time  time.Time
	Valid bool
}

// Scan 实现 sql.Scanner。
func (n *Nullable) Scan(src any) error {
	if n == nil {
		return fmt.Errorf("dbtime: Nullable 接收者为空")
	}
	switch value := src.(type) {
	case nil:
		n.Time = time.Time{}
		n.Valid = false
		return nil
	case time.Time:
		n.Time = value.UTC()
		n.Valid = true
		return nil
	case string:
		return n.scanText(value)
	case []byte:
		return n.scanText(string(value))
	default:
		return fmt.Errorf("dbtime: 不支持的时间扫描类型 %T", src)
	}
}

func (n *Nullable) scanText(value string) error {
	text := strings.TrimSpace(value)
	if text == "" {
		return fmt.Errorf("dbtime: 空字符串不是合法时间")
	}
	parsed, ok := ParseDB(text)
	if !ok {
		return fmt.Errorf("dbtime: 非法时间值 %q", truncate(text, 64))
	}
	n.Time = parsed
	n.Valid = true
	return nil
}

// API 返回毫秒 UTC `Z` 文本；NULL 返回 nil。
func (n Nullable) API() *string {
	if !n.Valid {
		return nil
	}
	value := API(n.Time)
	return &value
}

// API6 返回微秒 UTC `Z` 文本；NULL 返回 nil。
func (n Nullable) API6() *string {
	if !n.Valid {
		return nil
	}
	value := API6(n.Time)
	return &value
}

// APIString 返回毫秒 UTC `Z` 文本；NULL 返回空串。用于 DTO 的必填/可空
// 字符串字段，避免直接把 *string 解引用。
func (n Nullable) APIString() string {
	if !n.Valid {
		return ""
	}
	return API(n.Time)
}

// Ptr 返回时间指针；NULL 返回 nil。
func (n Nullable) Ptr() *time.Time {
	if !n.Valid {
		return nil
	}
	value := n.Time
	return &value
}

// OrZero 返回值或零时间。
func (n Nullable) OrZero() time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return n.Time
}

// SQLNow 返回 UTC 时钟的 SQL 文本表达式，供 text 时间列或诊断查询使用。
func SQLNow(p Precision) string {
	if p == Micros {
		return sqlNowMicros
	}
	return sqlNowMillis
}

// SQLText 返回把 timestamptz 列格式化为 UTC 文本的 SQL 表达式。
// column 必须是代码内可信的字面标识符，禁止拼接用户输入。
func SQLText(column string, p Precision) string {
	unit := `MS`
	if p == Micros {
		unit = `US`
	}
	return fmt.Sprintf(`to_char(%s AT TIME ZONE 'UTC', %s%s"Z"')`, column, sqlTextLayout, unit)
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
