package accountsbalance

// 余额身份摘要的有效代理目标解析单测（代理池设计 §5.2/§10.3）：实例 source
// 覆盖与两列冲突不选边。LoadBalanceInputDigests 的 SQL 取列与 J2 直读 reader
// 同列语义，绑定判定集中在 effectiveBalanceProxyTarget 纯函数（PG 面 E2E 由
// accounts 门面 list_balance_snapshot_test.go 与 oauthmgmt 池用例共同回归）。

import "testing"

func TestEffectiveBalanceProxyTarget(t *testing.T) {
	cases := []struct {
		name          string
		ownProfile    string
		ownPool       string
		sourceProfile string
		sourcePool    string
		wantProfile   string
		wantPool      string
		wantOK        bool
	}{
		{name: "无绑定", wantOK: true},
		{name: "自身直接代理", ownProfile: "prof-1", wantProfile: "prof-1", wantOK: true},
		{name: "自身池绑定", ownPool: "pool-1", wantPool: "pool-1", wantOK: true},
		{
			name:        "自身两列冲突不选边",
			ownProfile:  "prof-1",
			ownPool:     "pool-1",
			wantProfile: "",
			wantPool:    "",
			wantOK:      false,
		},
		{
			name:          "实例 source 两列冲突不选边",
			sourceProfile: "s-prof",
			sourcePool:    "s-pool",
			wantOK:        false,
		},
		{
			name:        "实例 source 池覆盖（实例自身直接代理不参与）",
			ownProfile:  "own-prof",
			sourcePool:  "s-pool",
			wantProfile: "",
			wantPool:    "s-pool",
			wantOK:      true,
		},
		{
			name:          "实例 source 代理覆盖（实例自身直接代理不参与）",
			ownProfile:    "own-prof",
			sourceProfile: "s-prof",
			wantProfile:   "s-prof",
			wantOK:        true,
		},
		{
			name:          "实例无 source 绑定回落实例自身直接代理",
			ownProfile:    "own-prof",
			sourceProfile: "",
			wantProfile:   "own-prof",
			wantOK:        true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			profileID, poolID, ok := effectiveBalanceProxyTarget(testCase.ownProfile, testCase.ownPool, testCase.sourceProfile, testCase.sourcePool)
			if ok != testCase.wantOK || profileID != testCase.wantProfile || poolID != testCase.wantPool {
				t.Fatalf("target = (%q,%q,%v), want (%q,%q,%v)", profileID, poolID, ok, testCase.wantProfile, testCase.wantPool, testCase.wantOK)
			}
		})
	}
}
