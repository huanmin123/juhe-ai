package main

// OAuth 跨进程共享状态的 gateway 侧适配（计划
// docs/plans/计划-20261106T024618000Z-网关与jobs跨进程共享状态改造.md §B1/§B2）。
//
// 锁/版本实体固定为 oauth 域（crossprocessstate.EntityOAuthRefreshLock /
// EntityAccountCredentialsVersion），账户 ID 由调用方传入。适配层只做实体绑定，
// 不含重试与业务判断：锁后端不可用由 oauthmgmt/gatewayruntimecache 按各自
// 既有路径回退。

import (
	"context"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/crossprocessstate"
)

// gatewayOAuthRefreshLock 适配 oauthmgmt.RefreshLock。
type gatewayOAuthRefreshLock struct {
	client *crossprocessstate.Client
}

// TryLock implements oauthmgmt.RefreshLock.
func (l gatewayOAuthRefreshLock) TryLock(ctx context.Context, accountID string, ttl time.Duration) (string, bool, error) {
	return l.client.TryLock(ctx, crossprocessstate.EntityOAuthRefreshLock, accountID, ttl)
}

// Unlock implements oauthmgmt.RefreshLock.
func (l gatewayOAuthRefreshLock) Unlock(ctx context.Context, accountID, token string) error {
	return l.client.Unlock(ctx, crossprocessstate.EntityOAuthRefreshLock, accountID, token)
}

// gatewayOAuthCredentialVersionBumper 适配 oauthmgmt.CredentialVersionBumper。
type gatewayOAuthCredentialVersionBumper struct {
	client *crossprocessstate.Client
}

// Bump implements oauthmgmt.CredentialVersionBumper.
func (b gatewayOAuthCredentialVersionBumper) Bump(ctx context.Context, accountID string, revision int64) error {
	_, err := b.client.BumpVersion(ctx, crossprocessstate.EntityAccountCredentialsVersion, accountID, revision)
	return err
}

// gatewayOAuthCredentialVersions 适配 gatewayruntimecache.CredentialVersionReader：
// 每次读取套 50ms 预算（计划 §5 硬约束 7），批量 MGET 同 namespace 键。
type gatewayOAuthCredentialVersions struct {
	client *crossprocessstate.Client
}

// CredentialVersions implements gatewayruntimecache.CredentialVersionReader.
func (v gatewayOAuthCredentialVersions) CredentialVersions(ctx context.Context, accountIDs []string) (map[string]int64, error) {
	readCtx, cancel := crossprocessstate.ReadContext(ctx)
	defer cancel()
	return v.client.Versions(readCtx, crossprocessstate.EntityAccountCredentialsVersion, accountIDs)
}

// gatewayRuntimeVersions 适配 gatewayruntimecache.RuntimeVersionReader：把凭据
// 版本与账户/API Key 运行态序号合并成一次 MGET。
type gatewayRuntimeVersions struct {
	client *crossprocessstate.Client
}

// RuntimeVersions implements gatewayruntimecache.RuntimeVersionReader.
func (v gatewayRuntimeVersions) RuntimeVersions(ctx context.Context, credentialAccountIDs, stateAccountIDs []string, apiKeyID string) (map[string]int64, map[string]int64, int64, error) {
	refs := make([]crossprocessstate.VersionRef, 0, len(credentialAccountIDs)+len(stateAccountIDs)+1)
	for _, id := range credentialAccountIDs {
		refs = append(refs, crossprocessstate.VersionRef{Entity: crossprocessstate.EntityAccountCredentialsVersion, ID: id})
	}
	for _, id := range stateAccountIDs {
		refs = append(refs, crossprocessstate.VersionRef{Entity: crossprocessstate.EntityGatewayStateVersion, ID: "account:" + id})
	}
	if apiKeyID != "" {
		refs = append(refs, crossprocessstate.VersionRef{Entity: crossprocessstate.EntityGatewayStateVersion, ID: "apikey:" + apiKeyID})
	}
	readCtx, cancel := crossprocessstate.ReadContext(ctx)
	defer cancel()
	versions, err := v.client.VersionsMulti(readCtx, refs)
	if err != nil {
		return nil, nil, 0, err
	}
	credentials := make(map[string]int64, len(credentialAccountIDs))
	for _, id := range credentialAccountIDs {
		credentials[id] = versions[crossprocessstate.VersionRef{Entity: crossprocessstate.EntityAccountCredentialsVersion, ID: id}]
	}
	state := make(map[string]int64, len(stateAccountIDs))
	for _, id := range stateAccountIDs {
		state[id] = versions[crossprocessstate.VersionRef{Entity: crossprocessstate.EntityGatewayStateVersion, ID: "account:" + id}]
	}
	apiKeyVersion := versions[crossprocessstate.VersionRef{Entity: crossprocessstate.EntityGatewayStateVersion, ID: "apikey:" + apiKeyID}]
	return credentials, state, apiKeyVersion, nil
}
