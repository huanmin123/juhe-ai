package gatewayhotquality

import (
	"fmt"
	"testing"
)

// T111：uniqueBoundedKeys 的倒序扫描 + 一次反转实现必须与旧前插实现逐项等价。
// 语义：从最新端扫描，重复 identity 保留最新一次出现；返回顺序为原 values 的
// 相对顺序；limit<=0 时仍只保留第一个命中项（旧实现 append 后判断的既有行为）。
func TestT111UniqueBoundedKeysSemantics(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		limit  int
		want   []string
	}{
		{name: "empty", values: nil, limit: 8, want: nil},
		{name: "non-nil empty", values: []string{}, limit: 8, want: nil},
		{name: "all unique", values: []string{"a", "b", "c"}, limit: 8, want: []string{"a", "b", "c"}},
		{name: "limit truncates newest", values: []string{"a", "b", "c", "a"}, limit: 2, want: []string{"c", "a"}},
		{name: "dedupe keeps latest in original order", values: []string{"a", "b", "a", "c"}, limit: 3, want: []string{"b", "a", "c"}},
		{name: "limit zero keeps one", values: []string{"a", "b"}, limit: 0, want: []string{"b"}},
		{name: "negative limit keeps one", values: []string{"a", "b"}, limit: -1, want: []string{"b"}},
		{name: "whitespace normalized before dedupe", values: []string{" a ", "a", "b"}, limit: 8, want: []string{"a", "b"}},
		{name: "limit above length", values: []string{"x", "x", "y"}, limit: 100, want: []string{"x", "y"}},
		{name: "sequence keeps newest window order", values: []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}, limit: 5, want: []string{"5", "6", "7", "8", "9"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := uniqueBoundedKeys(testCase.values, testCase.limit)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(testCase.want) {
				t.Fatalf("got %v, want %v", got, testCase.want)
			}
			for index := range got {
				if got[index] != testCase.want[index] {
					t.Fatalf("got %v, want %v", got, testCase.want)
				}
			}
			if len(testCase.values) == 0 && got != nil {
				t.Fatalf("empty input must stay nil, got %#v", got)
			}
		})
	}
}

func TestT111UniqueBoundedKeysNewestErrorWins(t *testing.T) {
	// 倒序扫描遇到非法 identity 立即失败，与旧实现一样报最新端的错误。
	if _, err := uniqueBoundedKeys([]string{"ok", " "}, 8); err == nil {
		t.Fatal("blank newest identity must fail")
	}
	// 旧端非法但最新端合法且 limit=1 时，旧实现会在到达旧端前 break，新实现同。
	got, err := uniqueBoundedKeys([]string{" ", "ok"}, 1)
	if err != nil || len(got) != 1 || got[0] != "ok" {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestT111UniqueBoundedKeysErrorReturnsNil(t *testing.T) {
	got, err := uniqueBoundedKeys([]string{" "}, 8)
	if err == nil {
		t.Fatal("blank identity must fail")
	}
	if got != nil {
		t.Fatalf("error path must return nil result, got %#v", got)
	}
}

func TestT111UniqueBoundedKeysDoesNotAliasInput(t *testing.T) {
	values := []string{"a", "b"}
	original := append([]string(nil), values...)
	got, err := uniqueBoundedKeys(values, 8)
	if err != nil {
		t.Fatal(err)
	}
	for index := range values {
		if values[index] != original[index] {
			t.Fatalf("input mutated at %d: %v != %v", index, values[index], original[index])
		}
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("got %v", got)
	}
	got[0] = "mutated"
	if values[0] != "a" {
		t.Fatalf("result aliases input: %v", values)
	}
}

func BenchmarkT111UniqueBoundedKeysMax(b *testing.B) {
	values := make([]string, SameTierExplorationIdentityCapacity)
	for index := range values {
		values[index] = fmt.Sprintf("identity-%04d", index)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		if _, err := uniqueBoundedKeys(values, SameTierExplorationIdentityCapacity); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkT111UniqueBoundedKeysScaling(b *testing.B) {
	for _, size := range []int{256, 512, 1024, SameTierExplorationIdentityCapacity} {
		b.Run(fmt.Sprintf("n=%d", size), func(b *testing.B) {
			values := make([]string, size)
			for index := range values {
				values[index] = fmt.Sprintf("identity-%04d", index)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				if _, err := uniqueBoundedKeys(values, size); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
