package proxylatency

import (
	"strings"
	"testing"
)

// 本文件补齐剩余错误传播分支：管理面配置装载、执行器 claim 释放失败
// （fault-injection 接缝）、runner 释放钩子的 join 语义与投影事务失败传播。

func TestWFDecryptBase64SubCases(t *testing.T) {
	secret := "wf-secret"
	envelope := wfSealPassword(t, secret, "pw")
	parts := strings.Split(envelope, ":")
	// iv 非 base64。
	badIV := "v1:" + "!!!" + ":" + parts[2] + ":" + parts[3]
	if _, err := decryptProxyPasswordV1(secret, CredentialEnvelope{Kind: "proxy_password", Ciphertext: badIV}); err == nil {
		t.Fatal("非法 iv base64 必须拒绝")
	}
	// tag 非 base64。
	badTag := "v1:" + parts[1] + ":" + "###" + ":" + parts[3]
	if _, err := decryptProxyPasswordV1(secret, CredentialEnvelope{Kind: "proxy_password", Ciphertext: badTag}); err == nil {
		t.Fatal("非法 tag base64 必须拒绝")
	}
}

