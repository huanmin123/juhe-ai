package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
)

func (s *Store) ResolveGatewayGroupAccess(ctx context.Context, input port.GatewayGroupAccessInput) (port.GatewayGroupAccess, bool, error) {
	input.GroupID = strings.TrimSpace(input.GroupID)
	input.SystemAccountID = strings.TrimSpace(input.SystemAccountID)
	if input.Now.IsZero() {
		input.Now = time.Now()
	}
	var access port.GatewayGroupAccess
	var accessType string
	var schedulingPolicy, authorizationID, limitsJSON, sourceType, sourceTeamID pgtype.Text
	var authorizationExpiresAt pgtype.Timestamptz
	err := s.pool.QueryRow(ctx, resolveGatewayGroupAccessSQL, input.GroupID, input.SystemAccountID, input.Now.UTC()).Scan(
		&access.GroupID,
		&access.GroupOwnerSystemAccountID,
		&access.ProviderCode,
		&access.GroupType,
		&schedulingPolicy,
		&accessType,
		&authorizationID,
		&authorizationExpiresAt,
		&limitsJSON,
		&sourceType,
		&sourceTeamID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.GatewayGroupAccess{}, false, nil
	}
	if err != nil {
		return port.GatewayGroupAccess{}, false, fmt.Errorf("resolve gateway group access: %w", err)
	}
	access.CallerSystemAccountID = input.SystemAccountID
	access.AccessType = port.GatewayGroupAccessType(accessType)
	access.SchedulingPolicyJSON = textValue(schedulingPolicy)
	access.GroupAuthorizationID = textValue(authorizationID)
	access.GroupAuthorizationExpiresAt = timestamptzPtr(authorizationExpiresAt)
	access.GroupAuthorizationLimitsJSON = textValue(limitsJSON)
	access.GroupAuthorizationSourceType = textValue(sourceType)
	access.GroupAuthorizationSourceTeamID = textValue(sourceTeamID)
	return access, true, nil
}

func (s *Store) ListGatewayAccountCandidates(ctx context.Context, input port.GatewayAccountCandidateListInput) ([]port.GatewayAccountCandidate, error) {
	normalizeGatewayCandidateInput(&input)
	limit := input.Limit
	if limit <= 0 || limit > port.GatewayAccountCandidateScanLimit {
		limit = port.GatewayAccountCandidateScanLimit
	}
	rows, err := s.pool.Query(
		ctx,
		listGatewayAccountCandidatesSQL,
		input.Access.GroupID,
		input.Access.GroupOwnerSystemAccountID,
		input.Access.CallerSystemAccountID,
		input.Access.ProviderCode,
		input.IncludeUnavailable,
		input.Now.UTC(),
		string(input.Access.AccessType),
		input.Access.GroupAuthorizationID,
		input.RequestedModel,
		input.EndpointFamily,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list gateway account candidates: %w", err)
	}
	defer rows.Close()
	candidates := make([]port.GatewayAccountCandidate, 0, limit)
	for rows.Next() {
		candidate, scanErr := scanGatewayAccountCandidate(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read gateway account candidates: %w", err)
	}
	return candidates, nil
}

type gatewayAccountCandidateScan func(...any) error

func scanGatewayAccountCandidate(scan gatewayAccountCandidateScan) (port.GatewayAccountCandidate, error) {
	var candidate port.GatewayAccountCandidate
	var accountAuthorizationID, proxyProfileID, availabilityScheduleJSON pgtype.Text
	var cooldownUntil, accountExpiresAt pgtype.Timestamptz
	var authorizationSourceAccountID, authorizationID, authorizationOwnerID pgtype.Text
	var authorizationExpiresAt pgtype.Timestamptz
	var authorizationLimitsJSON, authorizationSourceType, authorizationSourceTeamID pgtype.Text
	var resourceAccountID, resourceProviderCode, resourceProfileID, resourceProtocolCode, resourceProtocolVersion pgtype.Text
	var resourceType, resourceStatus, resourceCredentials, resourceProxyProfileID, resourceCompatibility pgtype.Text
	var resourceSchedulable pgtype.Bool
	var resourceCooldownUntil, resourceAccountExpiresAt pgtype.Timestamptz
	var resourceConcurrencyLimit pgtype.Int4
	var resourceConfigRevision pgtype.Int4
	var resourceDispatchRevision pgtype.Int8
	if err := scan(
		&candidate.AccountID,
		&candidate.SystemAccountID,
		&candidate.GroupID,
		&accountAuthorizationID,
		&candidate.LocalPriority,
		&candidate.LocalSuperPriorityEnabled,
		&candidate.LocalFallbackEnabled,
		&candidate.BindingCreatedAt,
		&candidate.ProviderCode,
		&candidate.ProviderProtocolProfileID,
		&candidate.ProtocolCode,
		&candidate.ProtocolVersion,
		&candidate.Name,
		&candidate.Type,
		&candidate.Status,
		&candidate.Schedulable,
		&candidate.ConcurrencyLimit,
		&candidate.Priority,
		&candidate.SuperPriorityEnabled,
		&candidate.FallbackEnabled,
		&candidate.ClientCompatibility,
		&candidate.CredentialsEncrypted,
		&proxyProfileID,
		&availabilityScheduleJSON,
		&cooldownUntil,
		&accountExpiresAt,
		&candidate.ConfigRevision,
		&candidate.DispatchRevision,
		&authorizationSourceAccountID,
		&authorizationID,
		&authorizationOwnerID,
		&authorizationExpiresAt,
		&authorizationLimitsJSON,
		&authorizationSourceType,
		&authorizationSourceTeamID,
		&resourceAccountID,
		&resourceProviderCode,
		&resourceProfileID,
		&resourceProtocolCode,
		&resourceProtocolVersion,
		&resourceType,
		&resourceStatus,
		&resourceSchedulable,
		&resourceCredentials,
		&resourceProxyProfileID,
		&resourceCooldownUntil,
		&resourceAccountExpiresAt,
		&resourceConcurrencyLimit,
		&resourceCompatibility,
		&resourceConfigRevision,
		&resourceDispatchRevision,
		&candidate.ModelRank,
	); err != nil {
		return port.GatewayAccountCandidate{}, fmt.Errorf("scan gateway account candidate: %w", err)
	}
	candidate.AccountAuthorizationID = textValue(accountAuthorizationID)
	candidate.ProxyProfileID = textValue(proxyProfileID)
	candidate.AvailabilityScheduleJSON = textValue(availabilityScheduleJSON)
	candidate.CooldownUntil = timestamptzPtr(cooldownUntil)
	candidate.AccountExpiresAt = timestamptzPtr(accountExpiresAt)
	candidate.AuthorizationSourceAccountID = textValue(authorizationSourceAccountID)
	candidate.AuthorizationID = textValue(authorizationID)
	candidate.AuthorizationOwnerSystemAccountID = textValue(authorizationOwnerID)
	candidate.AuthorizationExpiresAt = timestamptzPtr(authorizationExpiresAt)
	candidate.AuthorizationLimitsJSON = textValue(authorizationLimitsJSON)
	candidate.AuthorizationSourceType = textValue(authorizationSourceType)
	candidate.AuthorizationSourceTeamID = textValue(authorizationSourceTeamID)
	candidate.ResourceAccountID = textValue(resourceAccountID)
	candidate.ResourceProviderCode = textValue(resourceProviderCode)
	candidate.ResourceProviderProtocolProfileID = textValue(resourceProfileID)
	candidate.ResourceProtocolCode = textValue(resourceProtocolCode)
	candidate.ResourceProtocolVersion = textValue(resourceProtocolVersion)
	candidate.ResourceType = textValue(resourceType)
	candidate.ResourceStatus = textValue(resourceStatus)
	candidate.ResourceSchedulable = resourceSchedulable.Valid && resourceSchedulable.Bool
	candidate.ResourceCredentialsEncrypted = textValue(resourceCredentials)
	candidate.ResourceProxyProfileID = textValue(resourceProxyProfileID)
	candidate.ResourceCooldownUntil = timestamptzPtr(resourceCooldownUntil)
	candidate.ResourceAccountExpiresAt = timestamptzPtr(resourceAccountExpiresAt)
	if resourceConcurrencyLimit.Valid {
		candidate.ResourceConcurrencyLimit = int(resourceConcurrencyLimit.Int32)
	}
	candidate.ResourceClientCompatibility = textValue(resourceCompatibility)
	if resourceConfigRevision.Valid {
		candidate.ResourceConfigRevision = int(resourceConfigRevision.Int32)
	}
	if resourceDispatchRevision.Valid {
		candidate.ResourceDispatchRevision = resourceDispatchRevision.Int64
	}
	candidate.BindingCreatedAt = candidate.BindingCreatedAt.UTC()
	return candidate, nil
}

func normalizeGatewayCandidateInput(input *port.GatewayAccountCandidateListInput) {
	input.Access.GroupID = strings.TrimSpace(input.Access.GroupID)
	input.Access.GroupOwnerSystemAccountID = strings.TrimSpace(input.Access.GroupOwnerSystemAccountID)
	input.Access.CallerSystemAccountID = strings.TrimSpace(input.Access.CallerSystemAccountID)
	input.Access.ProviderCode = strings.TrimSpace(input.Access.ProviderCode)
	input.Access.GroupAuthorizationID = strings.TrimSpace(input.Access.GroupAuthorizationID)
	input.RequestedModel = strings.TrimSpace(input.RequestedModel)
	input.EndpointFamily = strings.TrimSpace(input.EndpointFamily)
	if input.Now.IsZero() {
		input.Now = time.Now()
	}
}

var _ port.GatewayAccountCandidateReader = (*Store)(nil)
