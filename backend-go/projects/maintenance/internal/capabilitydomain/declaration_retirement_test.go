package capabilitydomain

import (
	"testing"

	capdomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
)

// TestRetirementDomainsCoverFiveDomains 钉住 revision 30 清洗词表与共享包
// 六值域词表的关系：恰为六值去掉 chat 的五值，且 SQL 字面量逐值一致
// （扫描 / 凭据回写 / 连接行删除三处拼接同一常量）。
func TestRetirementDomainsCoverFiveDomains(t *testing.T) {
	allValid := map[string]bool{
		string(capdomain.DomainChat):      true,
		string(capdomain.DomainVideo):     true,
		string(capdomain.DomainAudio):     true,
		string(capdomain.DomainImage):     true,
		string(capdomain.DomainEmbedding): true,
		string(capdomain.DomainRerank):    true,
	}
	declared := map[string]bool{
		"image":     true,
		"audio":     true,
		"video":     true,
		"embedding": true,
		"rerank":    true,
	}
	for domain := range declared {
		if !allValid[domain] {
			t.Fatalf("清洗词表含未知域 %q", domain)
		}
	}
	if declared[string(capdomain.DomainChat)] {
		t.Fatal("清洗词表不得包含 chat 域（声明面保留）")
	}
	want := `'image', 'audio', 'video', 'embedding', 'rerank'`
	if retirementDomainsSQL != want {
		t.Fatalf("retirementDomainsSQL = %s, want %s", retirementDomainsSQL, want)
	}
}
