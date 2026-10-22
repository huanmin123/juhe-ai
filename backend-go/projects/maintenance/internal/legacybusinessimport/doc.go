// Package legacybusinessimport rebuilds an authoritative business database from
// a wiped, freshly ensured schema plus a whitelist copy of the legacy
// provider-era rows (供应商与上游协议面重构设计 §10).
//
// This is the "rebuild + backfill" cutover path approved for the 2026-10-22
// release: the target structure is always the HEAD schema produced by
// --ensure-schema, never a column-by-column in-place upgrade. Only business
// configuration rows (accounts, groups, routing, keys, authorizations,
// settings, teams, integrations, tags, quotas) are carried across. The
// historical usage/audit/statistics/model-check domains are deliberately NOT
// imported — the release explicitly discards them.
//
// Every write happens inside one transaction; any unsupported legacy row
// aborts the import before commit, so a partially rebuilt business database is
// never left behind.
package legacybusinessimport
