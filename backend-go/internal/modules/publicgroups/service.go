package publicgroups

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	DefaultGroupType             = "personal"
	GroupTypeHighConcurrency     = "high_concurrency"
	autoCreatedTargetDescription = "由公开接口自动创建"
	autoCreatedPasswordHash      = "go-public-auto-created-target-password-hash"
)

var (
	ErrTargetNotFound          = errors.New("public group target not found")
	ErrTargetDisabled          = errors.New("public group target disabled")
	ErrProviderNotFound        = errors.New("public group provider not found")
	ErrProviderDisabled        = errors.New("public group provider disabled")
	ErrGroupNotFound           = errors.New("public group not found")
	ErrDuplicateGroupName      = errors.New("public group duplicate name")
	ErrDefaultGroupReadonly    = errors.New("public group default readonly")
	ErrDefaultGroupDelete      = errors.New("public group default delete")
	ErrGroupProviderHasAccount = errors.New("public group provider has account")
	ErrRouteStrategyWouldLose  = errors.New("public group route strategy would lose group")
)

type Service struct {
	store      port.PublicGroupStore
	transactor port.PublicGroupTransactor
	now        func() time.Time
	newID      func(prefix string) string
}

type Options struct {
	Store      port.PublicGroupStore
	Transactor port.PublicGroupTransactor
	Now        func() time.Time
	NewID      func(prefix string) string
}

type Target struct {
	Username        string `json:"username"`
	DisplayName     string `json:"displayName"`
	SystemAccountID string `json:"systemAccountId"`
	Created         bool   `json:"created"`
}

type GroupSummary struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	ProviderCode string  `json:"providerCode"`
	Description  *string `json:"description"`
	Enabled      bool    `json:"enabled"`
	GroupType    string  `json:"groupType"`
	IsDefault    bool    `json:"isDefault"`
}

type GroupResponse struct {
	Source      string        `json:"source"`
	GeneratedAt string        `json:"generatedAt"`
	Action      string        `json:"action"`
	Target      Target        `json:"target"`
	Group       *GroupSummary `json:"group"`
}

type GroupListResponse struct {
	Source         string         `json:"source"`
	GeneratedAt    string         `json:"generatedAt"`
	Target         Target         `json:"target"`
	Page           int            `json:"page"`
	PageSize       int            `json:"pageSize"`
	PageUpperBound int            `json:"pageUpperBound"`
	HasMore        bool           `json:"hasMore"`
	Items          []GroupSummary `json:"items"`
}

type ListInput struct {
	TargetUsername string
	ProviderCode   string
	Keyword        string
	Page           int
	PageSize       int
}

type AddInput struct {
	TargetUsername    string
	TargetDisplayName string
	Name              string
	ProviderCode      string
	Description       *string
	Enabled           *bool
	GroupType         string
}

type UpdateInput struct {
	TargetUsername *string
	GroupID        string
	Name           *string
	ProviderCode   *string
	Description    OptionalString
	Enabled        *bool
	GroupType      *string
}

type DeleteInput struct {
	TargetUsername *string
	GroupID        string
}

type OptionalString struct {
	value *string
	set   bool
}

func NewService(opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	newID := opts.NewID
	if newID == nil {
		newID = func(prefix string) string {
			return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
	}
	return &Service{
		store:      opts.Store,
		transactor: opts.Transactor,
		now:        now,
		newID:      newID,
	}
}

func NewOptionalString(value *string, set bool) OptionalString {
	return OptionalString{value: value, set: set}
}

func (s OptionalString) Set() bool {
	return s.set
}

func (s OptionalString) Value() *string {
	return s.value
}

func (s *Service) List(ctx context.Context, input ListInput) (GroupListResponse, error) {
	target, err := s.requireTarget(ctx, input.TargetUsername)
	if err != nil {
		return GroupListResponse{}, err
	}
	page, err := s.store.ListPublicGroups(ctx, port.PublicGroupListInput{
		SystemAccountID: target.ID,
		ProviderCode:    strings.TrimSpace(input.ProviderCode),
		Keyword:         strings.TrimSpace(input.Keyword),
		Page:            input.Page,
		PageSize:        input.PageSize,
	})
	if err != nil {
		return GroupListResponse{}, err
	}
	items := make([]GroupSummary, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, publicGroupSummary(item))
	}
	return GroupListResponse{
		Source:         "stats",
		GeneratedAt:    s.generatedAt(),
		Target:         publicTarget(target),
		Page:           page.Page,
		PageSize:       page.PageSize,
		PageUpperBound: page.PageUpperBound,
		HasMore:        page.HasMore,
		Items:          items,
	}, nil
}

func (s *Service) Add(ctx context.Context, input AddInput) (GroupResponse, error) {
	var response GroupResponse
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		response, err = s.addOnce(ctx, input)
		if publicGroupAddRetryable(err) {
			continue
		}
		return response, err
	}
	if errors.Is(err, port.ErrPublicGroupDuplicateName) {
		return response, fmt.Errorf("%w: %s", ErrDuplicateGroupName, input.Name)
	}
	return response, err
}

func (s *Service) addOnce(ctx context.Context, input AddInput) (GroupResponse, error) {
	var response GroupResponse
	err := s.inTx(ctx, func(ctx context.Context, store port.PublicGroupStore) error {
		if err := s.assertProviderEnabled(ctx, store, input.ProviderCode); err != nil {
			return err
		}
		target, err := s.ensureTarget(ctx, store, input.TargetUsername, input.TargetDisplayName)
		if err != nil {
			return err
		}
		if err := assertTargetActive(target); err != nil {
			return err
		}
		existing, ok, err := store.FindExistingPublicGroupByName(ctx, target.ID, input.ProviderCode, input.Name)
		if err != nil {
			return err
		}
		if ok {
			response = groupResponse("existing", target, existing, s.generatedAt())
			return nil
		}
		enabled := true
		if input.Enabled != nil {
			enabled = *input.Enabled
		}
		groupType := normalizeGroupType(input.GroupType)
		group, err := store.CreatePublicGroup(ctx, port.PublicGroupCreateInput{
			ID:              s.newID("grp"),
			SystemAccountID: target.ID,
			Name:            input.Name,
			ProviderCode:    input.ProviderCode,
			Description:     input.Description,
			Enabled:         enabled,
			GroupType:       groupType,
			Now:             s.now().UTC(),
		})
		if errors.Is(err, port.ErrPublicGroupDuplicateName) {
			return port.ErrPublicGroupDuplicateName
		}
		if err != nil {
			return err
		}
		response = groupResponse("created", target, group, s.generatedAt())
		return nil
	})
	return response, err
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (GroupResponse, error) {
	var response GroupResponse
	err := s.inTx(ctx, func(ctx context.Context, store port.PublicGroupStore) error {
		current, target, err := s.groupAndTargetForWrite(ctx, store, input.GroupID, input.TargetUsername)
		if err != nil {
			return err
		}
		if current.IsDefault {
			return ErrDefaultGroupReadonly
		}

		next := current
		if input.Name != nil {
			next.Name = *input.Name
		}
		if input.ProviderCode != nil {
			if err := s.assertProviderEnabled(ctx, store, *input.ProviderCode); err != nil {
				return err
			}
			if *input.ProviderCode != current.ProviderCode {
				count, err := store.PublicGroupAccountCount(ctx, current.ID)
				if err != nil {
					return err
				}
				if count > 0 {
					return ErrGroupProviderHasAccount
				}
			}
			next.ProviderCode = *input.ProviderCode
		}
		if input.Description.Set() {
			next.Description = input.Description.Value()
		}
		if input.Enabled != nil {
			if current.Enabled && !*input.Enabled {
				if err := s.assertRouteStrategyCanLoseGroup(ctx, store, current.ID, current.Name, "停用分组"); err != nil {
					return err
				}
			}
			next.Enabled = *input.Enabled
		}
		if input.GroupType != nil {
			next.GroupType = normalizeGroupType(*input.GroupType)
		}

		updated, ok, err := store.UpdatePublicGroup(ctx, port.PublicGroupUpdateInput{
			ID:              current.ID,
			SystemAccountID: current.SystemAccountID,
			Name:            next.Name,
			ProviderCode:    next.ProviderCode,
			Description:     next.Description,
			Enabled:         next.Enabled,
			GroupType:       next.GroupType,
			Now:             s.now().UTC(),
		})
		if errors.Is(err, port.ErrPublicGroupDuplicateName) {
			return fmt.Errorf("%w: %s", ErrDuplicateGroupName, next.Name)
		}
		if err != nil {
			return err
		}
		if !ok {
			return ErrGroupNotFound
		}
		response = groupResponse("updated", target, updated, s.generatedAt())
		return nil
	})
	return response, err
}

func (s *Service) Delete(ctx context.Context, input DeleteInput) (GroupResponse, error) {
	var response GroupResponse
	err := s.inTx(ctx, func(ctx context.Context, store port.PublicGroupStore) error {
		current, target, err := s.groupAndTargetForWrite(ctx, store, input.GroupID, input.TargetUsername)
		if err != nil {
			return err
		}
		if current.IsDefault {
			return ErrDefaultGroupDelete
		}
		if err := s.assertRouteStrategyCanLoseGroup(ctx, store, current.ID, current.Name, "删除分组"); err != nil {
			return err
		}
		deleted, err := store.DeletePublicGroup(ctx, current.ID, current.SystemAccountID)
		if err != nil {
			return err
		}
		if !deleted {
			return ErrGroupNotFound
		}
		response = groupResponse("deleted", target, current, s.generatedAt())
		return nil
	})
	return response, err
}

func (s *Service) requireTarget(ctx context.Context, username string) (port.PublicGroupTarget, error) {
	target, ok, err := s.store.FindPublicGroupTargetByUsername(ctx, strings.TrimSpace(username))
	if err != nil {
		return port.PublicGroupTarget{}, err
	}
	if !ok {
		return port.PublicGroupTarget{}, fmt.Errorf("%w: %s", ErrTargetNotFound, strings.TrimSpace(username))
	}
	if err := assertTargetActive(target); err != nil {
		return port.PublicGroupTarget{}, err
	}
	return target, nil
}

func (s *Service) ensureTarget(ctx context.Context, store port.PublicGroupStore, username string, displayName string) (port.PublicGroupTarget, error) {
	username = strings.TrimSpace(username)
	target, ok, err := store.FindPublicGroupTargetByUsername(ctx, username)
	if err != nil {
		return port.PublicGroupTarget{}, err
	}
	if ok {
		return target, nil
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = username
	}
	return store.CreatePublicGroupTarget(ctx, port.PublicGroupTargetCreateInput{
		ID:           s.newID("sys"),
		Username:     username,
		DisplayName:  displayName,
		Description:  autoCreatedTargetDescription,
		PasswordHash: autoCreatedPasswordHash,
		Now:          s.now().UTC(),
	})
}

func (s *Service) groupAndTargetForWrite(ctx context.Context, store port.PublicGroupStore, groupID string, targetUsername *string) (port.PublicGroupSummary, port.PublicGroupTarget, error) {
	group, ok, err := store.FindPublicGroupByID(ctx, groupID)
	if err != nil {
		return port.PublicGroupSummary{}, port.PublicGroupTarget{}, err
	}
	if !ok {
		return port.PublicGroupSummary{}, port.PublicGroupTarget{}, ErrGroupNotFound
	}

	target, ok, err := targetByIDOrUsername(ctx, store, group.SystemAccountID, targetUsername)
	if err != nil {
		return port.PublicGroupSummary{}, port.PublicGroupTarget{}, err
	}
	if !ok {
		return port.PublicGroupSummary{}, port.PublicGroupTarget{}, ErrGroupNotFound
	}
	if err := assertTargetActive(target); err != nil {
		return port.PublicGroupSummary{}, port.PublicGroupTarget{}, err
	}
	return group, target, nil
}

func targetByIDOrUsername(ctx context.Context, store port.PublicGroupStore, ownerSystemAccountID string, targetUsername *string) (port.PublicGroupTarget, bool, error) {
	if targetUsername != nil {
		target, ok, err := store.FindPublicGroupTargetByUsername(ctx, *targetUsername)
		if err != nil || !ok {
			return port.PublicGroupTarget{}, false, err
		}
		if target.ID != ownerSystemAccountID {
			return port.PublicGroupTarget{}, false, nil
		}
		return target, true, nil
	}

	return store.FindPublicGroupTargetByID(ctx, ownerSystemAccountID)
}

func (s *Service) assertProviderEnabled(ctx context.Context, store port.PublicGroupStore, providerCode string) error {
	enabled, ok, err := store.ProviderEnabled(ctx, strings.TrimSpace(providerCode))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrProviderNotFound, strings.TrimSpace(providerCode))
	}
	if !enabled {
		return fmt.Errorf("%w: %s", ErrProviderDisabled, strings.TrimSpace(providerCode))
	}
	return nil
}

func (s *Service) assertRouteStrategyCanLoseGroup(ctx context.Context, store port.PublicGroupStore, groupID string, groupName string, action string) error {
	count, err := store.PublicGroupActiveRouteStrategyLossCount(ctx, groupID)
	if err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf(
			"%w: 无法%s“%s”：该分组仍是活跃策略路由的唯一可用启用分组，请先到策略路由中切换或新增启用分组，或删除这些策略路由后再操作。",
			ErrRouteStrategyWouldLose,
			action,
			groupName,
		)
	}
	return nil
}

func (s *Service) inTx(ctx context.Context, fn func(context.Context, port.PublicGroupStore) error) error {
	if s.transactor != nil {
		return s.transactor.PublicGroupInTx(ctx, fn)
	}
	return fn(ctx, s.store)
}

func publicGroupAddRetryable(err error) bool {
	return errors.Is(err, port.ErrPublicGroupTargetDuplicateUsername) ||
		errors.Is(err, port.ErrPublicGroupDuplicateName)
}

func assertTargetActive(target port.PublicGroupTarget) error {
	if target.Status != "active" {
		return fmt.Errorf("%w: %s", ErrTargetDisabled, target.Username)
	}
	return nil
}

func publicTarget(target port.PublicGroupTarget) Target {
	return Target{
		Username:        target.Username,
		DisplayName:     target.DisplayName,
		SystemAccountID: target.ID,
		Created:         target.Created,
	}
}

func publicGroupSummary(group port.PublicGroupSummary) GroupSummary {
	return GroupSummary{
		ID:           group.ID,
		Name:         group.Name,
		ProviderCode: group.ProviderCode,
		Description:  group.Description,
		Enabled:      group.Enabled,
		GroupType:    group.GroupType,
		IsDefault:    group.IsDefault,
	}
}

func groupResponse(action string, target port.PublicGroupTarget, group port.PublicGroupSummary, generatedAt string) GroupResponse {
	summary := publicGroupSummary(group)
	return GroupResponse{
		Source:      "stats",
		GeneratedAt: generatedAt,
		Action:      action,
		Target:      publicTarget(target),
		Group:       &summary,
	}
}

func (s *Service) generatedAt() string {
	return s.now().UTC().Format(time.RFC3339Nano)
}

func normalizeGroupType(value string) string {
	switch strings.TrimSpace(value) {
	case GroupTypeHighConcurrency:
		return GroupTypeHighConcurrency
	default:
		return DefaultGroupType
	}
}
