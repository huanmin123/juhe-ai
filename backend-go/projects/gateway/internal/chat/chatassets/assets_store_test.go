package chatassets

import (
	"database/sql"
	"strings"
	"testing"
)

// BUG-0232：ON CONFLICT DO UPDATE 的右值自引用必须表名限定——PG 对未限定
// 裸列报 42702 ambiguous。本测试固化生产语句形状，防止回归。
type recordingQueryer struct {
	query string
	args  []any
}

func (r *recordingQueryer) QueryRow(query string, args ...any) *sql.Row        { return nil }
func (r *recordingQueryer) Query(query string, args ...any) (*sql.Rows, error) { return nil, nil }
func (r *recordingQueryer) Exec(query string, args ...any) (sql.Result, error) {
	r.query = query
	r.args = args
	return nil, nil
}

func testPorts() Ports {
	bind := func(query string) string {
		var out strings.Builder
		index := 1
		for i := 0; i < len(query); i++ {
			if query[i] == '?' {
				out.WriteString("$" + string(rune('0'+index)))
				index++
			} else {
				out.WriteByte(query[i])
			}
		}
		return out.String()
	}
	table := func(name string) string { return "juhe_chat." + name }
	return Ports{Table: table, Bind: bind}
}

func TestIncrementAssetUserUsageQualifiesUpsertTarget(t *testing.T) {
	recorder := &recordingQueryer{}
	store := &AssetStore{ports: testPorts()}
	if err := store.incrementAssetUserUsage(recorder, "owner", 128, "now"); err != nil {
		t.Fatalf("err = %v", err)
	}
	table := "juhe_chat.chat_user_asset_usage"
	if !strings.Contains(recorder.query, "asset_bytes = "+table+".asset_bytes + ") ||
		!strings.Contains(recorder.query, "asset_count = "+table+".asset_count + 1") {
		t.Fatalf("upsert 右值未表名限定: %s", recorder.query)
	}
	if strings.Contains(recorder.query, "asset_bytes = asset_bytes") {
		t.Fatalf("upsert 右值存在未限定自引用: %s", recorder.query)
	}
}
