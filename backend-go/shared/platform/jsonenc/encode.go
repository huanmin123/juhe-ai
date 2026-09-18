// Package jsonenc 提供内部载荷 JSON 编码的统一实现。
// 收敛 circuitstore/gatewaycircuit 两份 encodeJSON 副本（评估文档 R1 取证；
// 两处入参均为纯数据结构，Marshal 失败不可达），统一取"忽略错误"语义，
// 现行行为不变。
package jsonenc

import (
	"encoding/json"
	"net/http"
)

// EncodeJSON 把内部载荷编码为 JSON 字符串。入参均为内部纯数据结构，
// Marshal 不会失败；为兼容既有"忽略错误"语义，失败时返回空串。
func EncodeJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// WriteJSON 设置 Content-Type 为 application/json、写状态码并编码响应体。
// 各调用方保留自己的错误 envelope 构造（charset 等差异是外部可见行为）。
func WriteJSON(w http.ResponseWriter, status int, value any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = w.Write(encoded)
	return err
}
