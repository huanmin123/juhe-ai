package proxyresolve

import "testing"

func TestAccountBindingEffective(t *testing.T) {
	cases := []struct {
		name   string
		bind   AccountBinding
		kind   BindingKind
		target string
		key    string
	}{
		{
			name: "ordinary none",
			bind: AccountBinding{AccountID: "a"},
			kind: BindingNone,
			key:  "a",
		},
		{
			name: "ordinary profile",
			bind: AccountBinding{AccountID: "a", ConfiguredProfileID: "p1"},
			kind: BindingProfile, target: "p1", key: "a",
		},
		{
			name: "ordinary pool",
			bind: AccountBinding{AccountID: "a", ConfiguredPoolID: "pool1"},
			kind: BindingPool, target: "pool1", key: "a",
		},
		{
			name: "ordinary conflict",
			bind: AccountBinding{AccountID: "a", ConfiguredProfileID: "p1", ConfiguredPoolID: "pool1"},
			kind: BindingConflict, key: "a",
		},
		{
			name: "instance source pool wins over instance profile",
			bind: AccountBinding{
				AccountID: "inst", SourceAccountID: "src", SourcePoolID: "pool1", ConfiguredProfileID: "instance-p1",
			},
			kind: BindingPool, target: "pool1", key: "src",
		},
		{
			name: "instance source profile wins over instance pool",
			bind: AccountBinding{
				AccountID: "inst", SourceAccountID: "src", SourceProfileID: "src-p1", ConfiguredPoolID: "pool1",
			},
			kind: BindingProfile, target: "src-p1", key: "src",
		},
		{
			name: "instance falls back to own profile",
			bind: AccountBinding{
				AccountID: "inst", SourceAccountID: "src", ConfiguredProfileID: "instance-p1",
			},
			kind: BindingProfile, target: "instance-p1", key: "src",
		},
		{
			name: "instance own pool is ignored",
			bind: AccountBinding{
				AccountID: "inst", SourceAccountID: "src", ConfiguredPoolID: "pool1",
			},
			kind: BindingNone, key: "src",
		},
		{
			name: "instance source pool ignores instance dual columns",
			bind: AccountBinding{
				AccountID: "inst", SourceAccountID: "src", SourcePoolID: "pool1",
				ConfiguredProfileID: "instance-p1", ConfiguredPoolID: "pool2",
			},
			kind: BindingPool, target: "pool1", key: "src",
		},
		{
			name: "instance source profile ignores instance dual columns",
			bind: AccountBinding{
				AccountID: "inst", SourceAccountID: "src", SourceProfileID: "src-p1",
				ConfiguredProfileID: "instance-p1", ConfiguredPoolID: "pool2",
			},
			kind: BindingProfile, target: "src-p1", key: "src",
		},
		{
			name: "instance configured conflict",
			bind: AccountBinding{
				AccountID: "inst", SourceAccountID: "src",
				ConfiguredProfileID: "instance-p1", ConfiguredPoolID: "pool1",
			},
			kind: BindingConflict, key: "src",
		},
		{
			name: "source conflict",
			bind: AccountBinding{
				AccountID: "inst", SourceAccountID: "src", SourcePoolID: "pool1", SourceProfileID: "src-p1",
			},
			kind: BindingConflict, key: "src",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, target := tc.bind.Effective()
			if kind != tc.kind || target != tc.target {
				t.Fatalf("Effective() = (%s, %q), want (%s, %q)", kind, target, tc.kind, tc.target)
			}
			if key := tc.bind.LeaseKey(); key != tc.key {
				t.Fatalf("LeaseKey() = %q, want %q", key, tc.key)
			}
		})
	}
}
