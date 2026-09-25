package circuitstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// RedisListDuePage is one parsed page of the listDue Lua script response.
// Fields are exported because the parsing is shared while the store-side
// scan loops stay in the owning modules.
type RedisListDuePage struct {
	ScopeKeys  []string
	Scanned    int64
	NextOffset int64
	Exhausted  bool
}

func ParseListDuePage(encoded string) (RedisListDuePage, error) {
	if encoded == "" {
		return RedisListDuePage{}, errors.New("Redis 账户电路 due 分页返回无效")
	}
	var parsed struct {
		ScopeKeys  *json.RawMessage `json:"scopeKeys"`
		Scanned    *int64           `json:"scanned"`
		NextOffset *int64           `json:"nextOffset"`
		Exhausted  *bool            `json:"exhausted"`
	}
	if err := json.Unmarshal([]byte(encoded), &parsed); err != nil {
		return RedisListDuePage{}, errors.New("Redis 账户电路 due 分页返回无效")
	}
	if parsed.ScopeKeys == nil || parsed.Scanned == nil || parsed.NextOffset == nil {
		return RedisListDuePage{}, errors.New("Redis 账户电路 due 分页 scopeKeys 无效")
	}
	if *parsed.Scanned < 0 || *parsed.NextOffset < 0 {
		return RedisListDuePage{}, errors.New("Redis 账户电路 due 分页游标无效")
	}
	// Lua cjson 把空数组编码为 `{}`（对象）：必须等价于空列表，否则空 due 页
	// 会被判无效（2026-09-25 生产 account-circuit-recovery 连续失败根因）。
	// 非空数组仍按旧语义容忍混合标量类型（fmt.Sprint 归一为字符串）。
	scopeKeys := make([]string, 0)
	trimmedScopeKeys := strings.TrimSpace(string(*parsed.ScopeKeys))
	if trimmedScopeKeys != "" && trimmedScopeKeys != "{}" && trimmedScopeKeys != "null" {
		var items []any
		if err := json.Unmarshal(*parsed.ScopeKeys, &items); err != nil {
			return RedisListDuePage{}, errors.New("Redis 账户电路 due 分页返回无效")
		}
		for _, item := range items {
			scopeKeys = append(scopeKeys, fmt.Sprintf("%v", item))
		}
	}
	exhausted := parsed.Exhausted != nil && *parsed.Exhausted
	return RedisListDuePage{ScopeKeys: scopeKeys, Scanned: *parsed.Scanned, NextOffset: *parsed.NextOffset, Exhausted: exhausted}, nil
}
