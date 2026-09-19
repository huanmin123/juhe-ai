// Package accountscore is the neutral shared-type layer of the accounts
// slice (REFACTOR-0005 设计 v2): the error trio, AccessScope, the credential
// value types, the crypto envelope, the SQL primitives and the endpoint-mode
// predicate family, plus the narrow StoreBase port the accounts subdomain
// packages consume instead of the facade Store.
//
// 依赖方向约束：所有人只 import accountscore，accountscore 不 import 任何
// accounts 门面或兄弟子域包（accountstest / accountsbalance / ...）。
// The facade package (internal/accounts) keeps type aliases and forwarding
// wrappers so the external accounts.XXX consumption surface stays unchanged.
package accountscore
