package openaicompat

// w9f 覆盖收尾：零覆盖的小函数与注入选项，生产逻辑零改动。

import (
	"testing"
)

func TestW9FImageGenerationProviderError(t *testing.T) {
	providerError := &ImageGenerationProviderError{Message: "上游图片生成失败", StatusCode: 502}
	if providerError.Error() != "上游图片生成失败" {
		t.Fatalf("error = %q", providerError.Error())
	}
}

func TestW9FStoreWithIDGeneratorOption(t *testing.T) {
	store := newTestStore(t)
	if store == nil {
		t.Fatal("store fixture")
	}
	sequence := 0
	generator := func(kind string) string {
		sequence++
		return kind + "_w9f_" + string(rune('a'+sequence))
	}
	applied := WithIDGenerator(generator)
	if applied == nil {
		t.Fatal("option factory")
	}
	// 直接应用到 store 校验注入生效。
	applied(store)
	if store.newID == nil {
		t.Fatal("id generator must be injected")
	}
	if got := store.newID("file"); got == "" || got[:4] != "file" {
		t.Fatalf("generated id = %q", got)
	}
}

func TestW9FParseInt64Edges(t *testing.T) {
	if got := parseInt64(" 42 "); got != 42 {
		t.Fatalf("trimmed = %d", got)
	}
	if got := parseInt64("not-a-number"); got != 0 {
		t.Fatalf("invalid = %d", got)
	}
	if got := parseInt64(""); got != 0 {
		t.Fatalf("empty = %d", got)
	}
}
