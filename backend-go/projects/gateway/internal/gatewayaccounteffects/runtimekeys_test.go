package gatewayaccounteffects

import (
	"testing"
)

func TestGatewayAccountRuntimeKey(t *testing.T) {
	tests := []struct {
		name    string
		account SuppressibleGatewayAccount
		want    string
		wantErr string
	}{
		{name: "owner 使用裸账户 id", account: SuppressibleGatewayAccount{ID: "acc-1"}, want: "acc-1"},
		{name: "account_authorized 带完整绑定", account: SuppressibleGatewayAccount{
			ID:                     "acc-2",
			AccountAccessType:      "account_authorized",
			BindingSystemAccountID: "sys-1",
			BoundGroupID:           "grp-1",
			AccountAuthorizationID: "authz-1",
		}, want: "acc-2:authorized:sys-1:grp-1:authz-1"},
		{name: "accessType authorized 等价", account: SuppressibleGatewayAccount{
			ID:                     "acc-3",
			AccessType:             "authorized",
			BindingSystemAccountID: "sys-2",
			BoundGroupID:           "grp-2",
			AccountAuthorizationID: "authz-2",
		}, want: "acc-3:authorized:sys-2:grp-2:authz-2"},
		{
			name: "缺少绑定上下文报错",
			account: SuppressibleGatewayAccount{
				ID:                     "acc-4",
				AccountAccessType:      "account_authorized",
				BindingSystemAccountID: "sys-1",
			},
			wantErr: "授权账户运行态键缺少绑定上下文",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GatewayAccountRuntimeKey(tt.account)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tt.want {
				t.Fatalf("runtimeKey = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRuntimeAccountIDFromKey(t *testing.T) {
	tests := []struct{ key, want string }{
		{"acc-1", "acc-1"},
		{"acc-1:authorized:sys:grp:authz", "acc-1"},
		{":leading", ""},
	}
	for _, tt := range tests {
		if got := RuntimeAccountIDFromKey(tt.key); got != tt.want {
			t.Fatalf("RuntimeAccountIDFromKey(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestGatewayAccountRuntimeClearKeys(t *testing.T) {
	includeBase := true
	excludeBase := false
	tests := []struct {
		name  string
		build func() []string
		want  []string
	}{
		{
			name:  "字符串 key",
			build: func() []string { return ClearKeysFromString(" key-1 ") },
			want:  []string{"key-1"},
		},
		{
			name:  "空字符串 key",
			build: func() []string { return ClearKeysFromString("  ") },
			want:  []string{},
		},
		{
			name: "owner 账户仅基础键",
			build: func() []string {
				return ClearKeysForAccount(SuppressibleGatewayAccount{ID: "acc-1"})
			},
			want: []string{"acc-1"},
		},
		{
			name: "授权账户含绑定键",
			build: func() []string {
				return ClearKeysForAccount(SuppressibleGatewayAccount{
					ID:                     "acc-2",
					AccountAccessType:      "account_authorized",
					BindingSystemAccountID: " sys ",
					BoundGroupID:           " grp ",
					AccountAuthorizationID: " authz ",
				})
			},
			want: []string{"acc-2", "acc-2:authorized:sys:grp:authz"},
		},
		{
			name: "clear target 排除基础键",
			build: func() []string {
				return GatewayAccountRuntimeClearTarget{
					AccountID:             "acc-3",
					IncludeBaseAccountKey: &excludeBase,
					AuthorizedBinding: &AuthorizedBinding{
						SystemAccountID:        "sys",
						GroupID:                "grp",
						AccountAuthorizationID: "authz",
					},
				}.ClearKeys()
			},
			want: []string{"acc-3:authorized:sys:grp:authz"},
		},
		{
			name: "clear target 默认包含基础键",
			build: func() []string {
				target := GatewayAccountRuntimeClearTarget{AccountID: "acc-4", IncludeBaseAccountKey: &includeBase}
				return target.ClearKeys()
			},
			want: []string{"acc-4"},
		},
		{
			name: "空 accountId 返回空",
			build: func() []string {
				return GatewayAccountRuntimeClearTarget{AccountID: "  "}.ClearKeys()
			},
			want: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.build()
			if len(got) != len(tt.want) {
				t.Fatalf("keys = %#v, want %#v", got, tt.want)
			}
			for index := range got {
				if got[index] != tt.want[index] {
					t.Fatalf("keys[%d] = %q, want %q", index, got[index], tt.want[index])
				}
			}
		})
	}
}
