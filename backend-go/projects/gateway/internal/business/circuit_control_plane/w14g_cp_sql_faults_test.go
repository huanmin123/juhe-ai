package circuitcontrolplane

import (
	"strings"
	"testing"
)

// w14g 保留面：validateIncident 纯函数分支（2026-10-24 存储形态收口：
// scripted SQLite 故障注入族随 sqlite 生产臂退役）。
// 不可达登记：store.go randomToken 的 rand.Read 错误分支与
// json.Marshal([]string) 分支在受支持平台不会失败。

func TestW14GCPValidateIncidentTextBranches(t *testing.T) {
	base := wkBaseIncident("w14g-validate", "a1", 1).Incident
	expectFailure := func(name string, mutate func(*Incident)) {
		t.Helper()
		v := base
		mutate(&v)
		if err := validateIncident(&v); err == nil {
			t.Fatalf("%s 必须校验失败", name)
		}
	}
	expectFailure("空 transition id", func(v *Incident) { v.TransitionID = "" })
	expectFailure("超长 runtime key", func(v *Incident) { v.AccountRuntimeKey = strings.Repeat("k", 2000) })
	expectFailure("超长 key fingerprint", func(v *Incident) {
		kind := "key"
		v.ScopeKind = kind
		v.KeyFingerprint = strPtr2(strings.Repeat("f", 300))
	})
	expectFailure("超长 protocol code", func(v *Incident) {
		v.ScopeKind = "protocol_model"
		v.ProtocolCode = strPtr2(strings.Repeat("p", 100))
		v.RequestLane = strPtr2("text")
		v.ModelFamily = strPtr2("bucket")
	})
	expectFailure("超长 request lane", func(v *Incident) {
		v.ScopeKind = "protocol_model"
		v.ProtocolCode = strPtr2("openai")
		v.RequestLane = strPtr2(strings.Repeat("l", 100))
		v.ModelFamily = strPtr2("bucket")
	})
	expectFailure("超长 model family", func(v *Incident) {
		v.ScopeKind = "protocol_model"
		v.ProtocolCode = strPtr2("openai")
		v.RequestLane = strPtr2("text")
		v.ModelFamily = strPtr2(strings.Repeat("m", 300))
	})
	expectFailure("超长 client model", func(v *Incident) {
		v.ScopeKind = "key_model"
		v.KeyFingerprint = strPtr2("fp")
		v.ClientModel = strPtr2(strings.Repeat("c", 300))
		v.CapabilityHash = strPtr2("cap")
		v.CredentialSourceAccountID = strPtr2("src")
		v.ClientEndpointFamily = strPtr2("chat")
		v.FinalUpstreamModel = strPtr2("final")
		v.UpstreamEndpointMode = strPtr2("mode")
	})
	expectFailure("超长 lease id", func(v *Incident) {
		v.LeaseID = strPtr2(strings.Repeat("l", 300))
		v.LeasePurpose = strPtr2("confirmation")
		v.LeaseOwnerRunID = strPtr2("run")
		v.LeaseUntilMS = int64Ptr2(1000)
		v.AttemptStartedAtMS = int64Ptr2(10)
		v.AttemptHardDeadlineMS = int64Ptr2(100)
	})
	expectFailure("超长 lease owner run id", func(v *Incident) {
		v.LeaseID = strPtr2("lease")
		v.LeasePurpose = strPtr2("confirmation")
		v.LeaseOwnerRunID = strPtr2(strings.Repeat("r", 300))
		v.LeaseUntilMS = int64Ptr2(1000)
		v.AttemptStartedAtMS = int64Ptr2(10)
		v.AttemptHardDeadlineMS = int64Ptr2(100)
	})
	expectFailure("超长 parent incident id", func(v *Incident) {
		v.ParentIncidentID = strPtr2(strings.Repeat("p", 300))
	})
	expectFailure("超长 caused by terminal outcome id", func(v *Incident) {
		v.CausedByTerminalOutcomeID = strPtr2(strings.Repeat("t", 300))
	})
}
