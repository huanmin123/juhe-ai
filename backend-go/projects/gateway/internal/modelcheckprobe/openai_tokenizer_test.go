package modelcheckprobe

import "testing"

func TestO200kTokenizerMatchesPinnedVersionAndCounts(t *testing.T) {
	tokenizer, err := NewO200kTokenizer()
	if err != nil {
		t.Fatal(err)
	}
	if tokenizer.Version() != "js-tiktoken@1.0.21:o200k_base" {
		t.Fatalf("version=%q", tokenizer.Version())
	}
	if count, err := tokenizer.Count("中文"); err != nil || count <= 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if count, err := tokenizer.Count(""); err != nil || count != 0 {
		t.Fatalf("empty count=%d err=%v", count, err)
	}
}
