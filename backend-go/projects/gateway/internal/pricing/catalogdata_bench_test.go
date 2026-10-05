package pricing

// 目录数据源性能基线（计划-20261005T230500000Z 性能核验）：
//   - JSON 加载（init 期一次性）成本量化——embed ReadFile + json.Unmarshal
//     + 反射还原，预期毫秒级、不在任何热路径；
//   - 请求链热路径基线——Find/List/全量投影，函数体迁移前后未变
//     （TestCatalogDataEquivalence 已证数据逐字段等价），此处钉住绝对值
//     供后续对照，防数据源形态再次演化时引入无感回归。
import (
	"fmt"
	"testing"
)

// BenchmarkCatalogDataLoadOnce 量化单供应商 JSON 加载（即 init 期成本构成）。
func BenchmarkCatalogDataLoadOnce(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := loadProviderCatalogModels("gpt"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCatalogDataLoadAll 量化九家全量加载（最坏启动成本上界）。
func BenchmarkCatalogDataLoadAll(b *testing.B) {
	for i := 0; i < b.N; i++ {
		for _, provider := range seedGenProviders {
			if _, err := loadProviderCatalogModels(provider); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkFindProviderModelPricing 请求链单模型查找（speed-first 路径命中）。
func BenchmarkFindProviderModelPricing(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if got := FindProviderModelPricing("gpt", "gpt-5.6-sol"); got == nil {
			b.Fatal("not found")
		}
	}
}

// BenchmarkListProviderModelPricing 目录列表路径（单 provider 全行投影）。
func BenchmarkListProviderModelPricing(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if got := ListProviderModelPricing("gpt"); len(got) == 0 {
			b.Fatal("empty")
		}
	}
}

// BenchmarkProjectAllCatalogRows 目录 API 最重路径：九家全行投影
// （providers 目录页按行调 buildProviderCatalogDisplay 前的 Pricing 构建）。
func BenchmarkProjectAllCatalogRows(b *testing.B) {
	for i := 0; i < b.N; i++ {
		count := 0
		for _, provider := range seedGenProviders {
			count += len(ListProviderModelPricing(provider))
		}
		if count < 200 {
			b.Fatal(fmt.Sprintf("rows = %d, 疑似目录加载异常（shutdown 过滤随日期浮动）", count))
		}
	}
}
