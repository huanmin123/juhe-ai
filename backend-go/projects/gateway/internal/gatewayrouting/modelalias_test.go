package gatewayrouting

import "testing"

// VolcengineTTSModelNamesMatch 等价组路由层行为（契约 §9.2「派发匹配等价」，
// 与 gatewaydispatch 派发候选过滤镜像共用）：路由选组阶段同样把占位名↔现役名
// 视为同一能力，多组场景下不因名字形态差异错剔分组。
func TestVolcengineTTSModelAliasRoutingMatch(t *testing.T) {
	cases := []struct {
		name       string
		requested  string
		supported  []string
		wantDirect bool
	}{
		{"声明占位名请求现役名", "seed-audio-1.0", []string{"doubao-tts"}, true},
		{"声明现役名请求占位名", "doubao-tts", []string{"seed-audio-1.0"}, true},
		{"同名不回归", "doubao-tts", []string{"doubao-tts"}, true},
		{"等价组外不放宽", "gpt-4o-mini", []string{"doubao-tts"}, false},
		{"大小写变体", "Seed-Audio-1.0", []string{"doubao-tts"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveGatewayAccountModelMatch(tc.requested, tc.supported); got != tc.wantDirect {
				t.Fatalf("direct match = %v, want %v", got, tc.wantDirect)
			}
			if got := isMappingAllowedBySupportedModels(tc.requested, tc.supported); got != tc.wantDirect {
				t.Fatalf("mapping allow = %v, want %v", got, tc.wantDirect)
			}
		})
	}
}

// VolcengineTTSModelAliasVariantsOf 是候选窗口 SQL 谓词展开的参数来源（契约
// §9.2「派发匹配等价」候选窗口层）：组内模型返回全部规范名成员（声明任一
// 名字的账户都命中 direct 档），组外模型退化为单元素自身（谓词维持 `= ?`
// 精确匹配、行为零变化）。若本函数退化为恒等单元素，fix H 的跨名窗口行为
// 会静默回归且现有同名路径测试全绿——本组用例即为该回归的钉子。
func TestVolcengineTTSModelAliasVariantsOf(t *testing.T) {
	cases := []struct {
		name       string
		requested  string
		wantCount  int
		wantDoubao bool
		wantSeed   bool
	}{
		{"现役名展开双成员", "seed-audio-1.0", 2, true, true},
		{"占位名展开双成员", "doubao-tts", 2, true, true},
		{"大小写变体展开双规范名", "Seed-Audio-1.0", 2, true, true},
		{"组外模型退化为单元素", "gpt-4o-mini", 1, false, false},
		{"空串退化为单元素", "", 1, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			variants := VolcengineTTSModelAliasVariantsOf(tc.requested)
			if len(variants) != tc.wantCount {
				t.Fatalf("variants = %v, want %d entries", variants, tc.wantCount)
			}
			hasDoubao, hasSeed := false, false
			for _, v := range variants {
				switch v {
				case "doubao-tts":
					hasDoubao = true
				case "seed-audio-1.0":
					hasSeed = true
				}
			}
			if hasDoubao != tc.wantDoubao || hasSeed != tc.wantSeed {
				t.Fatalf("variants = %v, doubao=%v seed=%v, want doubao=%v seed=%v",
					variants, hasDoubao, hasSeed, tc.wantDoubao, tc.wantSeed)
			}
		})
	}
}
