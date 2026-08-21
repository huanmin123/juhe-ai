package proxylatency

import "testing"

func TestSummarizeItems(t *testing.T) {
	for _, test := range []struct {
		name  string
		items []ItemResult
		want  OverallStatus
	}{
		{name: "failed wins", items: []ItemResult{{Status: ItemPassed}, {Status: ItemFailed}}, want: OverallFailed},
		{name: "warning wins", items: []ItemResult{{Status: ItemPassed}, {Status: ItemWarning}}, want: OverallWarning},
		{name: "passed plus unknown warns", items: []ItemResult{{Status: ItemPassed}, {Status: ItemUnknown}}, want: OverallWarning},
		{name: "only passed", items: []ItemResult{{Status: ItemPassed}}, want: OverallPassed},
		{name: "only unknown", items: []ItemResult{{Status: ItemUnknown}}, want: OverallUnknown},
		{name: "empty", want: OverallUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := SummarizeItems(test.items); got != test.want {
				t.Fatalf("SummarizeItems()=%q want %q", got, test.want)
			}
		})
	}
}
