package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestManagementAccountOptionLimit(t *testing.T) {
	tests := []struct {
		input int
		want  int
	}{
		{input: 0, want: 50},
		{input: -1, want: 50},
		{input: 1, want: 1},
		{input: 50, want: 50},
		{input: 51, want: 50},
	}
	for _, tt := range tests {
		if got := managementAccountOptionLimit(tt.input); got != tt.want {
			t.Fatalf("managementAccountOptionLimit(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeAccountNameSearchText(t *testing.T) {
	got := normalizeAccountNameSearchText("  ＡＢＣ  ")
	if got != "ABC" {
		t.Fatalf("normalizeAccountNameSearchText() = %q, want ABC", got)
	}
}

func TestAccountNameSearchQueryTerms(t *testing.T) {
	tests := []struct {
		name    string
		keyword string
		want    []string
	}{
		{name: "empty", keyword: "   ", want: nil},
		{name: "single rune", keyword: "中", want: []string{"中"}},
		{name: "two runes", keyword: "中文", want: []string{"中文"}},
		{name: "three gram", keyword: "Alpha", want: []string{"Alp", "lph", "pha"}},
		{name: "duplicate grams", keyword: "aaaa", want: []string{"aaa"}},
		{name: "nfkc", keyword: " ＡＢＣＤ ", want: []string{"ABC", "BCD"}},
		{name: "over max", keyword: strings.Repeat("a", maxAccountNameSearchLength+1), want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := accountNameSearchQueryTerms(tt.keyword)
			if !sameStringSet(got, tt.want) {
				t.Fatalf("accountNameSearchQueryTerms(%q) = %#v, want %#v", tt.keyword, got, tt.want)
			}
		})
	}
}

func TestAccountNameSearchGramsSkipsBlankTerms(t *testing.T) {
	got := accountNameSearchGrams("a  b", 2)
	if !sameStringSet(got, []string{"a ", " b"}) {
		t.Fatalf("accountNameSearchGrams() = %#v", got)
	}
}

func TestManagementAccountOptionsSQLUsesNameSearchIndex(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_account_options.sql")
	if err != nil {
		t.Fatalf("read account options query: %v", err)
	}
	sql := string(source)
	for _, want := range []string{
		"account_name_search_terms",
		"account_name_search_documents",
		"position(sqlc.arg(keyword_normalized)::text in documents.normalized_name) > 0",
		"HAVING COUNT(DISTINCT account_name_candidate_terms.term) = sqlc.arg(keyword_term_count)::int",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("account options query missing %q", want)
		}
	}
	for _, forbidden := range []string{" ILIKE ", "LIKE '%", "LIKE $"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("account options query should not use unbounded contains scan %q", forbidden)
		}
	}
}

func TestAccountNameSearchDocumentTerms(t *testing.T) {
	got := make([]string, 0)
	for length := 1; length <= accountNameSearchMaxGramLength; length++ {
		got = append(got, accountNameSearchGrams("测试", length)...)
	}
	if !sameStringSet(got, []string{"测", "试", "测试"}) {
		t.Fatalf("document terms = %#v", got)
	}
}

func sameStringSet(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]int, len(got))
	for _, value := range got {
		seen[value]++
	}
	for _, value := range want {
		seen[value]--
		if seen[value] < 0 {
			return false
		}
	}
	return true
}
