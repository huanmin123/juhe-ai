package circuitstore

// listavailability_sources.go 的 SQLite 读路径测试：usage 汇总、quota 直接 +
// team 继承双检查、余额快照、api key 池运行态汇总、circuit 摘要账本读。
// 复用 loader fixture（stats 与 business 同库），credentials_encrypted 列由
// 本文件按需补齐（SQLite ALTER TABLE，不影响其他测试）。
// 组B保留（不迁 PG）：loader 的 PG 臂与生产 schema 存在适配缺口（timestamptz
// 列裸扫 text 断言等，取证见 final_edges_test.go TestHydratePageFailClosed
// 注释），随 loader PG 臂修复批次再迁；测试名「SQLite」后缀在彼时一并更名。

import (
	"encoding/json"
)

// stubPoolCredentials 解码 {"keys":[...]} envelope 并按 keys 数量返回 key 池
// （loadAPIKeyRuntimeSummaries 的隔离与汇总依赖可 Mock）。
type stubPoolCredentials struct{}

func (stubPoolCredentials) DecryptCredentials(envelope string) (map[string]any, error) {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(envelope), &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func (stubPoolCredentials) AccountAPIKeyEntries(credentials map[string]any) []APIKeyPoolEntry {
	rawKeys, _ := credentials["keys"].([]any)
	entries := make([]APIKeyPoolEntry, 0, len(rawKeys))
	for index, key := range rawKeys {
		fingerprint, _ := key.(string)
		entries = append(entries, APIKeyPoolEntry{ID: fingerprint, Fingerprint: fingerprint, Index: index})
	}
	return entries
}

// newSourcesRow 构造最小 managementRow（owner 行默认，按需覆写授权面）。
func newSourcesRow(id string) managementRow {
	return managementRow{id: id, systemAccountID: "sys-1", ownerSystemAccountID: "sys-1"}
}

func authorizedSourcesRow(id string) managementRow {
	row := newSourcesRow(id)
	row.authorizationID.Valid = true
	row.authorizationID.String = "auth-" + id
	row.authorizationStatus.Valid = true
	row.authorizationStatus.String = "active"
	return row
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}
