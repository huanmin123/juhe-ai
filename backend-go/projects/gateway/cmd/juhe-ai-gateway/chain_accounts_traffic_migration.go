package main

// M11 traffic migration runtime handover bridge: adapts the gatewaysession
// AffinityService onto the accounts.TrafficRuntimeMigrator port. Node runs the
// same handover over the db-service IPC (migrateServerOpenAIAccountTrafficRuntime,
// db-service-ipc.ts:462-500); the Go single process always executes the local
// branch (migrateOpenAIAccountTrafficRuntimeLocal, db-service-ipc.ts:1414-1431):
//
//	1. migrateOpenAIAccountSessionAffinityAsync(source, target, affinityScope,
//	   { preferMigratedSessions })            — Redis failures rethrow;
//	2. rememberOpenAIAccountTrafficMigrationPreferenceAsync(source, target,
//	   preferenceScope, { throwOnRedisError: true }) — Redis failures throw;
//	3. return { migratedSessionCount }.
//
// The management route surfaces a migrator error as an explicit error outlet
// (the Node route catch renders the message), never a silent zero count.

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
)

// trafficRuntimeMigratorBridge is the composition-root adapter; the affinity
// service comes from the composed chain identity services (fail-fast assembly
// guarantees a non-nil instance).
type trafficRuntimeMigratorBridge struct {
	affinity *gatewaysession.AffinityService
}

// MigrateOpenAIAccountTrafficRuntime implements accounts.TrafficRuntimeMigrator.
// The management scope carries { systemAccountId, groupId } only (Node
// authorizedMigrationAffinityScope / trafficMigrationPreferenceScope); the api
// key dimension stays empty.
func (b trafficRuntimeMigratorBridge) MigrateOpenAIAccountTrafficRuntime(ctx context.Context, input accounts.TrafficRuntimeMigrationInput) (int, error) {
	affinityScope := trafficMigrationScope(input.AffinityScope)
	result, err := b.affinity.MigrateOpenAIAccountSessionAffinityAsync(ctx, input.SourceAccountID, input.TargetAccountID, affinityScope, gatewaysession.MigrationOptions{
		PreferMigratedSessions: input.PreferMigratedSessions,
	})
	if err != nil {
		return 0, err
	}
	if err := b.affinity.RememberOpenAIAccountTrafficMigrationPreferenceAsync(ctx, input.SourceAccountID, input.TargetAccountID, trafficMigrationScope(input.PreferenceScope), gatewaysession.TrafficMigrationPreferenceWriteOptions{
		ThrowOnRedisError: true,
	}); err != nil {
		return 0, err
	}
	return result.MigratedSessionCount, nil
}

// trafficMigrationScope maps the accounts scope onto the affinity scope.
func trafficMigrationScope(scope *accounts.TrafficMigrationScope) *gatewaysession.OpenAIGatewaySessionAffinityScope {
	if scope == nil {
		return nil
	}
	return &gatewaysession.OpenAIGatewaySessionAffinityScope{
		SystemAccountID: scope.SystemAccountID,
		GroupID:         scope.GroupID,
	}
}
