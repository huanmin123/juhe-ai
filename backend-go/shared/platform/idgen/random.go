// Package idgen 提供跨项目统一的随机 ID 生成原语。
// 收敛 8 个包各自的 newID（评估文档 R1 取证）：各副本的 ID 格式不同且已
// 持久化为主键，**格式不得变**；本包只统一共享的 crypto/rand 随机段原语，
// 各包保留自己的格式包装函数（前缀/分隔符/序列号是各自持久化契约）。
package idgen

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"time"
)

// readRandom 是随机读取的包级接缝（测试注入用）；生产恒为 crypto/rand.Read。
var readRandom = rand.Read

// RandomHex 返回 n 个随机字节 hex 编码的字符串（长度 2n）。
// crypto/rand 在现代平台不返回错误；极端失败时回退时间熵，保持长度不变。
func RandomHex(n int) string {
	if n <= 0 {
		return ""
	}
	buf := make([]byte, n)
	if _, err := readRandom(buf); err != nil {
		binary.LittleEndian.PutUint64(buf[0:8], uint64(time.Now().UnixNano()))
	}
	return hex.EncodeToString(buf)
}
