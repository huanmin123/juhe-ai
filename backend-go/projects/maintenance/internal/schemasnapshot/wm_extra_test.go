package schemasnapshot

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"testing"
)

// 本文件补齐 stableNormalize 的值域分支、nullable/null helpers 与
// collectOneRow 的 rows.Err() 错误传播路径。

type wmNormalizeCase struct {
	name    string
	value   any
	want    string
	wantErr string
}

func TestWMStableNormalizeCoversScalarAndCompositeArms(t *testing.T) {
	cases := []wmNormalizeCase{
		{name: "uint arm", value: struct {
			U uint `json:"u"`
		}{U: 7}, want: `{"u":7}`},
		{name: "map arm", value: struct {
			M map[string]int `json:"m"`
		}{M: map[string]int{"b": 2, "a": 1}}, want: `{"m":{"a":1,"b":2}}`},
		{name: "array arm", value: struct {
			A [2]int `json:"a"`
		}{A: [2]int{1, 2}}, want: `{"a":[1,2]}`},
		{name: "missing json tag", value: struct {
			NoTag int
		}{NoTag: 1}, wantErr: "缺少 json tag"},
		{name: "byte slice rejected", value: struct {
			B []byte `json:"b"`
		}{B: []byte("x")}, wantErr: "不支持 []byte"},
		{name: "non string map key rejected", value: struct {
			M map[int]int `json:"m"`
		}{M: map[int]int{1: 1}}, wantErr: "map key 非 string"},
		{name: "unsupported kind rejected", value: struct {
			C chan int `json:"c"`
		}{C: make(chan int)}, wantErr: "不支持的值类型"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := StableJSON(testCase.value)
			if testCase.wantErr != "" {
				if err == nil || !containsWMText(err.Error(), testCase.wantErr) {
					t.Fatalf("期望错误包含 %q，实际: %v (got %q)", testCase.wantErr, err, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("StableJSON: %v", err)
			}
			if got != testCase.want {
				t.Fatalf("StableJSON=%s want %s", got, testCase.want)
			}
		})
	}
}

func containsWMText(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || len(needle) == 0 || indexWM(haystack, needle) >= 0)
}

func indexWM(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestWMNullableDigestAndNullStringPointer(t *testing.T) {
	if nullableDigest(nil) != nil {
		t.Fatal("nil 定义不得产生 digest")
	}
	digest := nullableDigest("CREATE INDEX x")
	if digest == nil || len(*digest) != 64 {
		t.Fatalf("非空定义必须产生 sha256 指针: %v", digest)
	}
	if *digest != DigestDefinition("CREATE INDEX x") {
		t.Fatal("nullableDigest 必须复用 DigestDefinition")
	}
	if nullStringPointer(sql.NullString{}) != nil {
		t.Fatal("无效 NullString 必须映射 nil")
	}
	text := nullStringPointer(sql.NullString{Valid: true, String: "10.0.0.9"})
	if text == nil || *text != "10.0.0.9" {
		t.Fatalf("有效 NullString 必须映射指针: %v", text)
	}
	if nullStringOrEmpty(sql.NullString{}) != nil {
		t.Fatal("无效 NullString 必须映射 nil any")
	}
	if got := nullStringOrEmpty(sql.NullString{Valid: true, String: "PRIMARY KEY (id)"}); got != "PRIMARY KEY (id)" {
		t.Fatalf("有效 NullString 必须映射文本: %v", got)
	}
}

// wmErrAfterRow 是首次迭代即失败的驱动行集：database/sql 只在 Next 返回
// 非 EOF 错误时累积 rows.Err()，这里覆盖 collectOneRow 的“行集不可读”
// 错误传播路径。
type wmErrAfterRow struct {
	started bool
	err     error
}

func (r *wmErrAfterRow) Columns() []string { return []string{"name", "oid", "serverAddress", "serverPort"} }
func (r *wmErrAfterRow) Close() error      { return nil }
func (r *wmErrAfterRow) Next(dest []driver.Value) error {
	if r.started {
		return io.EOF
	}
	r.started = true
	return r.err
}

type wmErrAfterRowConn struct{ rows *wmErrAfterRow }

func (c *wmErrAfterRowConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("wm fake: Prepare 不应被调用")
}
func (c *wmErrAfterRowConn) Close() error              { return nil }
func (c *wmErrAfterRowConn) Begin() (driver.Tx, error) { return nil, errors.New("wm fake: Begin 不应被调用") }
func (c *wmErrAfterRowConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if containsWMText(query, "current_database()") {
		return c.rows, nil
	}
	return nil, fmt.Errorf("wm fake: unexpected query %.60s", query)
}

type wmErrAfterRowConnector struct{ rows *wmErrAfterRow }

func (c wmErrAfterRowConnector) Connect(context.Context) (driver.Conn, error) {
	return &wmErrAfterRowConn{rows: c.rows}, nil
}
func (c wmErrAfterRowConnector) Driver() driver.Driver { return wmErrAfterRowDriver{} }

type wmErrAfterRowDriver struct{}

func (wmErrAfterRowDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("wm fake: 请使用 sql.OpenDB")
}

func TestWMCollectOneRowPropagatesRowsError(t *testing.T) {
	db := sql.OpenDB(wmErrAfterRowConnector{rows: &wmErrAfterRow{err: errors.New("wm 连接断开")}})
	defer db.Close()
	var name, oid string
	found, err := collectOneRow(context.Background(), db, identitySQL, func(rows *sql.Rows) error {
		return rows.Scan(&name, &oid, new(*string), new(*int))
	})
	if found || err == nil || !containsWMText(err.Error(), "wm 连接断开") {
		t.Fatalf("行集不可读必须以错误上抛且不得误报成功: found=%t err=%v", found, err)
	}
}
