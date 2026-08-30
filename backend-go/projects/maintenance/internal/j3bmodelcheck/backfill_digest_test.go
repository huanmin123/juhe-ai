package j3bmodelcheck

import "testing"

func TestNormalizeValuePreservesNullAndSQLiteValueTypes(t *testing.T) {
	cases := []struct {
		name  string
		left  any
		right any
	}{
		{name: "null versus literal marker", left: nil, right: "<nil>"},
		{name: "text versus integer", left: "1", right: int64(1)},
		{name: "integer versus float", left: int64(1), right: float64(1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if normalizeValue(tc.left) == normalizeValue(tc.right) {
				t.Fatalf("values collapsed: left=%q right=%q", normalizeValue(tc.left), normalizeValue(tc.right))
			}
		})
	}
	if normalizeValue("same") != normalizeValue("same") {
		t.Fatal("identical strings must remain stable")
	}
	if normalizeValue("payload") != normalizeValue([]byte("payload")) {
		t.Fatal("text and driver byte representations must remain canonical-equivalent")
	}
}
