package pricing

import "testing"

// HasCatalogDirectPrice 是账户绑定与网关目录 priced 过滤的唯一判据（DR-P7）：
// 只有基础直价或静态媒体价可绑定；仅缓存价/仅档位价不保证默认流量可结算。
func TestHasCatalogDirectPriceRequiresBaseRate(t *testing.T) {
	zero := 0.0
	positive := 1.5
	cacheOnly := 2.0
	cases := []struct {
		name string
		set  PriceSet
		want bool
	}{
		{name: "空价", set: PriceSet{}, want: false},
		{name: "显式零输入价", set: PriceSet{InputUsdPer1M: &zero}, want: true},
		{name: "输入价", set: PriceSet{InputUsdPer1M: &positive}, want: true},
		{name: "输出价", set: PriceSet{OutputUsdPer1M: &positive}, want: true},
		{name: "图片 token 价", set: PriceSet{ImageOutputUsdPer1M: &positive}, want: true},
		{name: "按张价", set: PriceSet{OutputUsdPerImage: &positive}, want: true},
		{name: "仅缓存写价", set: PriceSet{CacheWriteUsdPer1M: &cacheOnly}, want: false},
		{name: "仅缓存读价", set: PriceSet{CachedInputUsdPer1M: &cacheOnly}, want: false},
		{name: "仅缓存存储价", set: PriceSet{CacheStorageUsdPer1MPerHour: &cacheOnly}, want: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := HasCatalogDirectPrice("gpt", "predicate-test-model", testCase.set); got != testCase.want {
				t.Fatalf("HasCatalogDirectPrice=%v want %v", got, testCase.want)
			}
		})
	}
	// 静态媒体价（catalogdata）仍可计价：DR-P7 只收紧档位/缓存，不误伤 TTS/音频/视频。
	// glm/cogtts 是只有 ttsInputCostPerChar、没有任何基础直价列的真实在售 TTS 行，
	// 必须由静态层放行；volcengine/seed-tts-2.0 覆盖同一分支的另一厂商快照。
	for _, media := range [][2]string{{"glm", "cogtts"}, {"volcengine", "seed-tts-2.0"}} {
		if !HasCatalogDirectPrice(media[0], media[1], PriceSet{}) {
			t.Fatalf("static media price must remain directly priced: %s/%s", media[0], media[1])
		}
	}
}
