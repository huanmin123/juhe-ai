package chat

import "testing"

type t33Catalog struct{}

func (t33Catalog) ListAccountsForGroup(string, string, string, string) []ChatTransportAccount {
	return nil
}

func (t33Catalog) ListChatPinnedAccountsForGroup(string, string, string) []ChatTransportAccount {
	return nil
}

func (t33Catalog) ListProviderCatalog(string, string) []ProviderModelCatalogItem {
	return nil
}

func (t33Catalog) ListAccountSupportedModels(accountID, systemAccountID string) []ProviderModelCatalogItem {
	if accountID != "acc-generic" || systemAccountID != "sys-owner" {
		return nil
	}
	return []ProviderModelCatalogItem{{
		Model:                 "generic-public",
		SupportedAPIProtocols: []string{"chat_completions"},
	}}
}

// T33：generic_http 账户（vendor 为空）从 account_supported_models 的真实
// models 行取目录，不再因空 provider 直接跳过；真实 vendor 路径不受影响。
func TestLoadChatModelCatalogForScopeGenericAccount(t *testing.T) {
	rt := &chatRoutes{deps: &Deps{
		ModelCatalog:        t33Catalog{},
		AccountModelCatalog: t33Catalog{},
	}}
	accounts, catalog := rt.loadChatModelCatalogForScope(&chatBindingScope{accounts: []ChatTransportAccount{{
		ID:           "acc-generic",
		ProviderCode: "",
	}}}, "sys-owner", "")
	if len(accounts) != 1 {
		t.Fatalf("accounts = %+v", accounts)
	}
	if len(catalog) != 1 || catalog[0].Model != "generic-public" {
		t.Fatalf("catalog = %+v, want account-scoped model row", catalog)
	}
}
