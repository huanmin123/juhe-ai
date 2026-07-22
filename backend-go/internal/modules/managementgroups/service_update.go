package managementgroups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
)

var (
	ErrGroupDefaultReadonly     = errors.New("默认分组不允许修改")
	ErrGroupProviderHasAccounts = errors.New("已有账户的分组不允许修改供应商")
)

type UpdateInput struct {
	ActorSystemAccountID string
	ActorRole            string
	SystemAccountID      string
	SelfOnly             bool
	GroupID              string
	HasName              bool
	Name                 string
	HasProviderCode      bool
	ProviderCode         string
	HasDescription       bool
	Description          *string
	HasEnabled           bool
	Enabled              bool
	HasGroupType         bool
	GroupType            string
	HasSchedulingPolicy  bool
	SchedulingPolicy     *SchedulingPolicyInput
}

type UpdateResult struct {
	Before                   port.ManagementGroupMutationSummary
	Group                    DetailResult
	AccessType               string
	OwnerSystemAccountID     string
	EffectiveSystemAccountID string
	GroupAuthorizationID     string
}

type UpdateRejectedError struct {
	Message string
}

func (e *UpdateRejectedError) Error() string {
	return e.Message
}

func UpdateRejectedMessage(err error) (string, bool) {
	var rejected *UpdateRejectedError
	if !errors.As(err, &rejected) {
		return "", false
	}
	return rejected.Message, true
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (UpdateResult, error) {
	writer, err := s.groupUpdater()
	if err != nil {
		return UpdateResult{}, err
	}
	effectiveSystemAccountID, canAccessAll, includeSystemAccountFields, err := managementGroupUpdateScope(input)
	if err != nil {
		return UpdateResult{}, err
	}

	name := strings.TrimSpace(input.Name)
	if input.HasName && name == "" {
		return UpdateResult{}, &ValidationError{Message: "分组名称不能为空"}
	}
	providerCode := strings.TrimSpace(input.ProviderCode)
	if input.HasProviderCode && providerCode == "" {
		return UpdateResult{}, &ValidationError{Message: "供应商不能为空"}
	}
	description := input.Description
	if input.HasDescription {
		description = normalizeDescription(input.Description)
	}
	groupType := input.GroupType
	if input.HasGroupType {
		groupType, err = normalizeGroupType(input.GroupType)
		if err != nil {
			return UpdateResult{}, err
		}
	}

	defaultPolicy, err := normalizeSchedulingPolicy(nil)
	if err != nil {
		return UpdateResult{}, err
	}
	defaultPolicyJSON, err := encodeSchedulingPolicy(defaultPolicy)
	if err != nil {
		return UpdateResult{}, err
	}
	var schedulingPolicyJSON *string
	if input.HasSchedulingPolicy {
		policy, err := normalizeSchedulingPolicy(input.SchedulingPolicy)
		if err != nil {
			return UpdateResult{}, err
		}
		encoded, err := encodeSchedulingPolicy(policy)
		if err != nil {
			return UpdateResult{}, err
		}
		schedulingPolicyJSON = &encoded
	}

	updated, err := writer.UpdateManagementGroup(ctx, port.ManagementGroupUpdateInput{
		GroupID:                     input.GroupID,
		ActorSystemAccountID:        strings.TrimSpace(input.ActorSystemAccountID),
		CanAccessAll:                canAccessAll,
		EffectiveSystemAccountID:    effectiveSystemAccountID,
		HasName:                     input.HasName,
		Name:                        name,
		HasProviderCode:             input.HasProviderCode,
		ProviderCode:                providerCode,
		HasDescription:              input.HasDescription,
		Description:                 description,
		HasEnabled:                  input.HasEnabled,
		Enabled:                     input.Enabled,
		HasGroupType:                input.HasGroupType,
		GroupType:                   groupType,
		HasSchedulingPolicy:         input.HasSchedulingPolicy,
		SchedulingPolicyJSON:        schedulingPolicyJSON,
		DefaultSchedulingPolicyJSON: defaultPolicyJSON,
		UpdatedAt:                   s.now().UTC(),
	})
	if err != nil {
		return UpdateResult{}, mapManagementGroupUpdateError(err, providerCode, name)
	}

	reason := GroupUpdatedReason
	if updated.AccessType == "authorized" {
		reason = GroupAuthorizationSettingsUpdatedReason
	} else {
		s.invalidateGroupLookup(ctx)
	}
	s.invalidateRuntimeWithReason(ctx, reason)
	group, err := s.Detail(ctx, DetailInput{
		ActorSystemAccountID: input.ActorSystemAccountID,
		ActorRole:            input.ActorRole,
		SystemAccountID:      input.SystemAccountID,
		SelfOnly:             input.SelfOnly,
		GroupID:              input.GroupID,
	})
	if err != nil {
		return UpdateResult{}, err
	}
	if includeSystemAccountFields && group.SystemAccountID == "" {
		group.SystemAccountID = updated.OwnerSystemAccountID
	}
	return UpdateResult{
		Before:                   updated.Before,
		Group:                    group,
		AccessType:               updated.AccessType,
		OwnerSystemAccountID:     updated.OwnerSystemAccountID,
		EffectiveSystemAccountID: updated.EffectiveSystemAccountID,
		GroupAuthorizationID:     updated.GroupAuthorizationID,
	}, nil
}

func (s *Service) groupUpdater() (port.ManagementGroupUpdater, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management group store is required")
	}
	writer, ok := s.store.(port.ManagementGroupUpdater)
	if !ok {
		return nil, fmt.Errorf("management group updater store is required")
	}
	return writer, nil
}

func managementGroupUpdateScope(input UpdateInput) (string, bool, bool, error) {
	actorSystemAccountID := strings.TrimSpace(input.ActorSystemAccountID)
	if actorSystemAccountID == "" || strings.TrimSpace(input.GroupID) == "" {
		return "", false, false, ErrGroupNotFound
	}
	if input.SelfOnly || !managementGroupListAdminRole(input.ActorRole) {
		return actorSystemAccountID, false, false, nil
	}
	systemAccountID := input.SystemAccountID
	if systemAccountID == "all" {
		systemAccountID = ""
	}
	return systemAccountID, systemAccountID == "", true, nil
}

func encodeSchedulingPolicy(policy SchedulingPolicy) (string, error) {
	encoded, err := json.Marshal(policy)
	if err != nil {
		return "", fmt.Errorf("encode management group scheduling policy: %w", err)
	}
	return string(encoded), nil
}

func mapManagementGroupUpdateError(err error, providerCode string, name string) error {
	switch {
	case errors.Is(err, port.ErrManagementGroupNotFound):
		return ErrGroupNotFound
	case errors.Is(err, port.ErrManagementGroupDefaultReadonly):
		return ErrGroupDefaultReadonly
	case errors.Is(err, port.ErrManagementGroupProviderHasAccounts):
		return ErrGroupProviderHasAccounts
	case errors.Is(err, port.ErrManagementGroupProviderNotFound):
		return &ProviderNotFoundError{Code: providerCode}
	case errors.Is(err, port.ErrManagementGroupProviderDisabled):
		return &ProviderDisabledError{Code: providerCode}
	case errors.Is(err, port.ErrManagementGroupNameExists):
		return &NameExistsError{Name: managementGroupUpdateConflictName(err, name)}
	case errors.Is(err, port.ErrManagementGroupAuthorizedFields):
		return &UpdateRejectedError{Message: managementGroupWrappedMessage(err, port.ErrManagementGroupAuthorizedFields)}
	case errors.Is(err, port.ErrManagementGroupRouteStrategyWouldLose):
		return &UpdateRejectedError{Message: managementGroupWrappedMessage(err, port.ErrManagementGroupRouteStrategyWouldLose)}
	default:
		return err
	}
}

func managementGroupUpdateConflictName(err error, fallback string) string {
	name := strings.TrimSpace(fallback)
	message := strings.TrimSpace(err.Error())
	prefix := port.ErrManagementGroupNameExists.Error() + ":"
	if strings.HasPrefix(message, prefix) {
		if wrappedName := strings.TrimSpace(strings.TrimPrefix(message, prefix)); wrappedName != "" {
			return wrappedName
		}
	}
	return name
}

func managementGroupWrappedMessage(err error, sentinel error) string {
	message := strings.TrimSpace(err.Error())
	prefix := sentinel.Error() + ":"
	if strings.HasPrefix(message, prefix) {
		message = strings.TrimSpace(strings.TrimPrefix(message, prefix))
	}
	if message == "" || message == sentinel.Error() {
		return "更新分组失败"
	}
	return message
}
