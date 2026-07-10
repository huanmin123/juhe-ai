package publicgroups

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

var publicGroupsTestNow = time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)

func TestAddReturnsExistingGroupIdempotently(t *testing.T) {
	store := newFakePublicGroupStore()
	store.providers["openai"] = true
	store.putTarget(activeTarget("sys_1", "alice", "Alice"))
	store.putGroup(port.PublicGroupSummary{
		ID:              "grp_existing",
		SystemAccountID: "sys_1",
		Name:            "默认分组",
		ProviderCode:    "openai",
		Enabled:         true,
		GroupType:       DefaultGroupType,
	})
	service, tx := newPublicGroupsTestService(store)

	resp, err := service.Add(context.Background(), AddInput{
		TargetUsername: "alice",
		Name:           "默认分组",
		ProviderCode:   "openai",
		GroupType:      GroupTypeHighConcurrency,
	})

	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if tx.calls != 1 {
		t.Fatalf("transaction calls = %d, want 1", tx.calls)
	}
	if resp.Action != "existing" || resp.Group == nil || resp.Group.ID != "grp_existing" {
		t.Fatalf("response = %+v, want existing grp_existing", resp)
	}
	if len(store.createGroupInputs) != 0 {
		t.Fatalf("CreatePublicGroup calls = %d, want 0", len(store.createGroupInputs))
	}
	if len(store.createTargetInputs) != 0 {
		t.Fatalf("CreatePublicGroupTarget calls = %d, want 0", len(store.createTargetInputs))
	}
}

func TestAddAutoCreatesTargetAndGroup(t *testing.T) {
	store := newFakePublicGroupStore()
	store.providers["openai"] = true
	service, _ := newPublicGroupsTestService(store)
	description := "自动创建的公开分组"

	resp, err := service.Add(context.Background(), AddInput{
		TargetUsername:    "  bob  ",
		TargetDisplayName: "  Bob User  ",
		Name:              "新增分组",
		ProviderCode:      "openai",
		Description:       &description,
	})

	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if resp.Action != "created" || resp.Group == nil {
		t.Fatalf("response = %+v, want created group", resp)
	}
	if resp.Target.Username != "bob" || resp.Target.DisplayName != "Bob User" || !resp.Target.Created {
		t.Fatalf("target = %+v, want trimmed auto-created target", resp.Target)
	}
	if resp.GeneratedAt != publicGroupsTestNow.Format(time.RFC3339Nano) {
		t.Fatalf("GeneratedAt = %q", resp.GeneratedAt)
	}
	if got, want := len(store.createTargetInputs), 1; got != want {
		t.Fatalf("CreatePublicGroupTarget calls = %d, want %d", got, want)
	}
	targetInput := store.createTargetInputs[0]
	if targetInput.ID != "sys_test_1" || targetInput.Username != "bob" || targetInput.DisplayName != "Bob User" {
		t.Fatalf("target create input = %+v", targetInput)
	}
	if targetInput.Description != autoCreatedTargetDescription || targetInput.PasswordHash != autoCreatedPasswordHash {
		t.Fatalf("target bootstrap fields = %+v", targetInput)
	}
	if got, want := len(store.createGroupInputs), 1; got != want {
		t.Fatalf("CreatePublicGroup calls = %d, want %d", got, want)
	}
	groupInput := store.createGroupInputs[0]
	if groupInput.ID != "grp_test_2" || groupInput.SystemAccountID != "sys_test_1" {
		t.Fatalf("group create ids = %+v", groupInput)
	}
	if groupInput.Name != "新增分组" || groupInput.ProviderCode != "openai" || !groupInput.Enabled {
		t.Fatalf("group create input = %+v", groupInput)
	}
	if groupInput.GroupType != DefaultGroupType {
		t.Fatalf("GroupType = %q, want %q", groupInput.GroupType, DefaultGroupType)
	}
}

func TestAddRecoversWhenConcurrentTargetCreateWins(t *testing.T) {
	store := newFakePublicGroupStore()
	store.providers["openai"] = true
	store.targetOnCreateErr = activeTarget("sys_concurrent", "bob", "Bob")
	store.createTargetErr = port.ErrPublicGroupTargetDuplicateUsername
	service, _ := newPublicGroupsTestService(store)

	resp, err := service.Add(context.Background(), AddInput{
		TargetUsername:    "bob",
		TargetDisplayName: "Bob",
		Name:              "新增分组",
		ProviderCode:      "openai",
	})

	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if resp.Action != "created" || resp.Target.SystemAccountID != "sys_concurrent" || resp.Target.Created {
		t.Fatalf("response target/action = %+v", resp)
	}
	if got, want := len(store.createTargetInputs), 1; got != want {
		t.Fatalf("CreatePublicGroupTarget calls = %d, want %d", got, want)
	}
	if got, want := len(store.createGroupInputs), 1; got != want {
		t.Fatalf("CreatePublicGroup calls = %d, want %d", got, want)
	}
	if store.createGroupInputs[0].SystemAccountID != "sys_concurrent" {
		t.Fatalf("group create input = %+v", store.createGroupInputs[0])
	}
}

func TestAddReturnsExistingWhenConcurrentGroupCreateWins(t *testing.T) {
	store := newFakePublicGroupStore()
	store.providers["openai"] = true
	store.putTarget(activeTarget("sys_1", "alice", "Alice"))
	store.groupOnCreateErr = port.PublicGroupSummary{
		ID:              "grp_concurrent",
		SystemAccountID: "sys_1",
		Name:            "福利",
		ProviderCode:    "openai",
		Enabled:         true,
		GroupType:       DefaultGroupType,
	}
	store.createGroupErr = port.ErrPublicGroupDuplicateName
	service, _ := newPublicGroupsTestService(store)

	resp, err := service.Add(context.Background(), AddInput{
		TargetUsername: "alice",
		Name:           "福利",
		ProviderCode:   "openai",
	})

	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if resp.Action != "existing" || resp.Group == nil || resp.Group.ID != "grp_concurrent" {
		t.Fatalf("response = %+v, want existing concurrent group", resp)
	}
	if got, want := len(store.createGroupInputs), 1; got != want {
		t.Fatalf("CreatePublicGroup calls = %d, want %d", got, want)
	}
}

func TestAddRejectsDisabledProviderBeforeCreatingTarget(t *testing.T) {
	store := newFakePublicGroupStore()
	store.providers["openai"] = false
	service, _ := newPublicGroupsTestService(store)

	_, err := service.Add(context.Background(), AddInput{
		TargetUsername: "new-user",
		Name:           "新增分组",
		ProviderCode:   "openai",
	})

	if !errors.Is(err, ErrProviderDisabled) {
		t.Fatalf("Add() error = %v, want ErrProviderDisabled", err)
	}
	if len(store.createTargetInputs) != 0 {
		t.Fatalf("CreatePublicGroupTarget calls = %d, want 0", len(store.createTargetInputs))
	}
	if len(store.createGroupInputs) != 0 {
		t.Fatalf("CreatePublicGroup calls = %d, want 0", len(store.createGroupInputs))
	}
}

func TestUpdateRejectsDefaultGroupReadonly(t *testing.T) {
	store := newFakePublicGroupStore()
	store.putTarget(activeTarget("sys_1", "alice", "Alice"))
	store.putGroup(port.PublicGroupSummary{
		ID:              "grp_default",
		SystemAccountID: "sys_1",
		Name:            "默认分组",
		ProviderCode:    "openai",
		Enabled:         true,
		GroupType:       DefaultGroupType,
		IsDefault:       true,
	})
	service, _ := newPublicGroupsTestService(store)
	name := "新名称"

	_, err := service.Update(context.Background(), UpdateInput{
		GroupID: "grp_default",
		Name:    &name,
	})

	if !errors.Is(err, ErrDefaultGroupReadonly) {
		t.Fatalf("Update() error = %v, want ErrDefaultGroupReadonly", err)
	}
	if len(store.updateGroupInputs) != 0 {
		t.Fatalf("UpdatePublicGroup calls = %d, want 0", len(store.updateGroupInputs))
	}
	if len(store.routeLossCountCalls) != 0 {
		t.Fatalf("route strategy checks = %d, want 0", len(store.routeLossCountCalls))
	}
}

func TestUpdateRejectsProviderChangeWhenGroupHasAccounts(t *testing.T) {
	store := newFakePublicGroupStore()
	store.providers["openai"] = true
	store.providers["anthropic"] = true
	store.putTarget(activeTarget("sys_1", "alice", "Alice"))
	store.putGroup(port.PublicGroupSummary{
		ID:              "grp_1",
		SystemAccountID: "sys_1",
		Name:            "业务分组",
		ProviderCode:    "openai",
		Enabled:         true,
		GroupType:       DefaultGroupType,
	})
	store.accountCounts["grp_1"] = 2
	service, _ := newPublicGroupsTestService(store)
	providerCode := "anthropic"

	_, err := service.Update(context.Background(), UpdateInput{
		GroupID:      "grp_1",
		ProviderCode: &providerCode,
	})

	if !errors.Is(err, ErrGroupProviderHasAccount) {
		t.Fatalf("Update() error = %v, want ErrGroupProviderHasAccount", err)
	}
	if got, want := store.accountCountCalls, []string{"grp_1"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("account count calls = %#v, want %#v", got, want)
	}
	if len(store.updateGroupInputs) != 0 {
		t.Fatalf("UpdatePublicGroup calls = %d, want 0", len(store.updateGroupInputs))
	}
}

func TestDeleteRejectsRouteStrategyProtection(t *testing.T) {
	store := newFakePublicGroupStore()
	store.putTarget(activeTarget("sys_1", "alice", "Alice"))
	store.putGroup(port.PublicGroupSummary{
		ID:              "grp_1",
		SystemAccountID: "sys_1",
		Name:            "业务分组",
		ProviderCode:    "openai",
		Enabled:         true,
		GroupType:       DefaultGroupType,
	})
	store.routeLossCounts["grp_1"] = 1
	service, _ := newPublicGroupsTestService(store)

	_, err := service.Delete(context.Background(), DeleteInput{GroupID: "grp_1"})

	if !errors.Is(err, ErrRouteStrategyWouldLose) {
		t.Fatalf("Delete() error = %v, want ErrRouteStrategyWouldLose", err)
	}
	if got, want := store.routeLossCountCalls, []string{"grp_1"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("route strategy checks = %#v, want %#v", got, want)
	}
	if len(store.deleteGroupCalls) != 0 {
		t.Fatalf("DeletePublicGroup calls = %d, want 0", len(store.deleteGroupCalls))
	}
}

func TestListPaginatesAndFiltersGroups(t *testing.T) {
	store := newFakePublicGroupStore()
	store.putTarget(activeTarget("sys_1", "owner", "Owner"))
	store.putTarget(activeTarget("sys_2", "other", "Other"))
	store.putGroup(port.PublicGroupSummary{ID: "grp_1", SystemAccountID: "sys_1", Name: "Alpha primary", ProviderCode: "openai", Enabled: true, GroupType: DefaultGroupType})
	store.putGroup(port.PublicGroupSummary{ID: "grp_2", SystemAccountID: "sys_1", Name: "Alpha azure", ProviderCode: "azure", Enabled: true, GroupType: DefaultGroupType})
	store.putGroup(port.PublicGroupSummary{ID: "grp_3", SystemAccountID: "sys_1", Name: "Beta", ProviderCode: "openai", Enabled: true, GroupType: DefaultGroupType})
	store.putGroup(port.PublicGroupSummary{ID: "grp_4", SystemAccountID: "sys_1", Name: "Alpha backup", ProviderCode: "openai", Enabled: false, GroupType: GroupTypeHighConcurrency})
	store.putGroup(port.PublicGroupSummary{ID: "grp_5", SystemAccountID: "sys_2", Name: "Alpha other", ProviderCode: "openai", Enabled: true, GroupType: DefaultGroupType})
	service, _ := newPublicGroupsTestService(store)

	resp, err := service.List(context.Background(), ListInput{
		TargetUsername: "  owner  ",
		ProviderCode:   "  openai  ",
		Keyword:        "  alpha  ",
		Page:           2,
		PageSize:       1,
	})

	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got, want := len(store.listInputs), 1; got != want {
		t.Fatalf("ListPublicGroups calls = %d, want %d", got, want)
	}
	listInput := store.listInputs[0]
	if listInput.SystemAccountID != "sys_1" || listInput.ProviderCode != "openai" || listInput.Keyword != "alpha" {
		t.Fatalf("list input = %+v, want trimmed filters for sys_1/openai/alpha", listInput)
	}
	if resp.Page != 2 || resp.PageSize != 1 || resp.PageUpperBound != 2 || resp.HasMore {
		t.Fatalf("pagination response = page %d size %d upper %d hasMore %v", resp.Page, resp.PageSize, resp.PageUpperBound, resp.HasMore)
	}
	if got, want := len(resp.Items), 1; got != want {
		t.Fatalf("items length = %d, want %d", got, want)
	}
	if resp.Items[0].ID != "grp_4" || resp.Items[0].GroupType != GroupTypeHighConcurrency || resp.Items[0].Enabled {
		t.Fatalf("item[0] = %+v, want disabled high-concurrency grp_4", resp.Items[0])
	}
}

func newPublicGroupsTestService(store *fakePublicGroupStore) (*Service, *fakePublicGroupTransactor) {
	tx := &fakePublicGroupTransactor{store: store}
	seq := 0
	service := NewService(Options{
		Store:      store,
		Transactor: tx,
		Now:        func() time.Time { return publicGroupsTestNow },
		NewID: func(prefix string) string {
			seq++
			return prefix + "_test_" + strconv.Itoa(seq)
		},
	})
	return service, tx
}

func activeTarget(id string, username string, displayName string) port.PublicGroupTarget {
	return port.PublicGroupTarget{
		ID:          id,
		Username:    username,
		DisplayName: displayName,
		Status:      "active",
	}
}

type fakePublicGroupTransactor struct {
	store *fakePublicGroupStore
	calls int
}

func (t *fakePublicGroupTransactor) PublicGroupInTx(ctx context.Context, fn func(context.Context, port.PublicGroupStore) error) error {
	t.calls++
	return fn(ctx, t.store)
}

type fakePublicGroupStore struct {
	targetsByUsername map[string]port.PublicGroupTarget
	targetsByID       map[string]port.PublicGroupTarget
	providers         map[string]bool
	groupsByID        map[string]port.PublicGroupSummary
	groupOrder        []string
	accountCounts     map[string]int64
	routeLossCounts   map[string]int64

	createTargetErr   error
	targetOnCreateErr port.PublicGroupTarget
	createGroupErr    error
	groupOnCreateErr  port.PublicGroupSummary

	createTargetInputs []port.PublicGroupTargetCreateInput
	createGroupInputs  []port.PublicGroupCreateInput
	updateGroupInputs  []port.PublicGroupUpdateInput
	deleteGroupCalls   []string
	listInputs         []port.PublicGroupListInput

	accountCountCalls    []string
	routeLossCountCalls  []string
	providerEnabledCalls []string
}

func newFakePublicGroupStore() *fakePublicGroupStore {
	return &fakePublicGroupStore{
		targetsByUsername: map[string]port.PublicGroupTarget{},
		targetsByID:       map[string]port.PublicGroupTarget{},
		providers:         map[string]bool{},
		groupsByID:        map[string]port.PublicGroupSummary{},
		accountCounts:     map[string]int64{},
		routeLossCounts:   map[string]int64{},
	}
}

func (s *fakePublicGroupStore) putTarget(target port.PublicGroupTarget) {
	s.targetsByUsername[target.Username] = target
	s.targetsByID[target.ID] = target
}

func (s *fakePublicGroupStore) putGroup(group port.PublicGroupSummary) {
	if _, exists := s.groupsByID[group.ID]; !exists {
		s.groupOrder = append(s.groupOrder, group.ID)
	}
	s.groupsByID[group.ID] = group
}

func (s *fakePublicGroupStore) FindPublicGroupTargetByUsername(_ context.Context, username string) (port.PublicGroupTarget, bool, error) {
	target, ok := s.targetsByUsername[username]
	return target, ok, nil
}

func (s *fakePublicGroupStore) FindPublicGroupTargetByID(_ context.Context, id string) (port.PublicGroupTarget, bool, error) {
	target, ok := s.targetsByID[id]
	return target, ok, nil
}

func (s *fakePublicGroupStore) CreatePublicGroupTarget(_ context.Context, input port.PublicGroupTargetCreateInput) (port.PublicGroupTarget, error) {
	s.createTargetInputs = append(s.createTargetInputs, input)
	if s.createTargetErr != nil {
		if s.targetOnCreateErr.ID != "" {
			s.putTarget(s.targetOnCreateErr)
		}
		return port.PublicGroupTarget{}, s.createTargetErr
	}
	target := port.PublicGroupTarget{
		ID:          input.ID,
		Username:    input.Username,
		DisplayName: input.DisplayName,
		Status:      "active",
		Created:     true,
	}
	s.putTarget(target)
	return target, nil
}

func (s *fakePublicGroupStore) ProviderEnabled(_ context.Context, providerCode string) (bool, bool, error) {
	s.providerEnabledCalls = append(s.providerEnabledCalls, providerCode)
	enabled, ok := s.providers[providerCode]
	return enabled, ok, nil
}

func (s *fakePublicGroupStore) ListPublicGroups(_ context.Context, input port.PublicGroupListInput) (port.PublicGroupListPage, error) {
	s.listInputs = append(s.listInputs, input)
	page := input.Page
	if page <= 0 {
		page = 1
	}
	pageSize := input.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	matches := make([]port.PublicGroupSummary, 0, len(s.groupOrder))
	for _, id := range s.groupOrder {
		group := s.groupsByID[id]
		if group.SystemAccountID != input.SystemAccountID {
			continue
		}
		if input.ProviderCode != "" && group.ProviderCode != input.ProviderCode {
			continue
		}
		if input.Keyword != "" && !publicGroupsTestContainsFold(group.Name, input.Keyword) && !publicGroupsTestContainsFold(publicGroupsTestStringValue(group.Description), input.Keyword) {
			continue
		}
		matches = append(matches, group)
	}
	pageUpperBound := 0
	if len(matches) > 0 {
		pageUpperBound = (len(matches) + pageSize - 1) / pageSize
	}
	start := (page - 1) * pageSize
	if start > len(matches) {
		start = len(matches)
	}
	end := start + pageSize
	if end > len(matches) {
		end = len(matches)
	}
	return port.PublicGroupListPage{
		Items:          append([]port.PublicGroupSummary(nil), matches[start:end]...),
		Page:           page,
		PageSize:       pageSize,
		PageUpperBound: pageUpperBound,
		HasMore:        page < pageUpperBound,
	}, nil
}

func (s *fakePublicGroupStore) FindPublicGroupByID(_ context.Context, groupID string) (port.PublicGroupSummary, bool, error) {
	group, ok := s.groupsByID[groupID]
	return group, ok, nil
}

func (s *fakePublicGroupStore) FindExistingPublicGroupByName(_ context.Context, systemAccountID string, providerCode string, name string) (port.PublicGroupSummary, bool, error) {
	for _, id := range s.groupOrder {
		group := s.groupsByID[id]
		if group.SystemAccountID == systemAccountID && group.ProviderCode == providerCode && group.Name == name {
			return group, true, nil
		}
	}
	return port.PublicGroupSummary{}, false, nil
}

func (s *fakePublicGroupStore) CreatePublicGroup(_ context.Context, input port.PublicGroupCreateInput) (port.PublicGroupSummary, error) {
	s.createGroupInputs = append(s.createGroupInputs, input)
	if s.createGroupErr != nil {
		if s.groupOnCreateErr.ID != "" {
			s.putGroup(s.groupOnCreateErr)
		}
		return port.PublicGroupSummary{}, s.createGroupErr
	}
	if _, ok, _ := s.FindExistingPublicGroupByName(context.Background(), input.SystemAccountID, input.ProviderCode, input.Name); ok {
		return port.PublicGroupSummary{}, port.ErrPublicGroupDuplicateName
	}
	group := port.PublicGroupSummary{
		ID:              input.ID,
		SystemAccountID: input.SystemAccountID,
		Name:            input.Name,
		ProviderCode:    input.ProviderCode,
		Description:     input.Description,
		Enabled:         input.Enabled,
		GroupType:       input.GroupType,
		CreatedAt:       input.Now,
		UpdatedAt:       input.Now,
	}
	s.putGroup(group)
	return group, nil
}

func (s *fakePublicGroupStore) UpdatePublicGroup(_ context.Context, input port.PublicGroupUpdateInput) (port.PublicGroupSummary, bool, error) {
	s.updateGroupInputs = append(s.updateGroupInputs, input)
	current, ok := s.groupsByID[input.ID]
	if !ok || current.SystemAccountID != input.SystemAccountID {
		return port.PublicGroupSummary{}, false, nil
	}
	for _, id := range s.groupOrder {
		group := s.groupsByID[id]
		if group.ID != input.ID && group.SystemAccountID == input.SystemAccountID && group.ProviderCode == input.ProviderCode && group.Name == input.Name {
			return port.PublicGroupSummary{}, false, port.ErrPublicGroupDuplicateName
		}
	}
	current.Name = input.Name
	current.ProviderCode = input.ProviderCode
	current.Description = input.Description
	current.Enabled = input.Enabled
	current.GroupType = input.GroupType
	current.UpdatedAt = input.Now
	s.groupsByID[input.ID] = current
	return current, true, nil
}

func (s *fakePublicGroupStore) DeletePublicGroup(_ context.Context, groupID string, systemAccountID string) (bool, error) {
	s.deleteGroupCalls = append(s.deleteGroupCalls, groupID)
	group, ok := s.groupsByID[groupID]
	if !ok || group.SystemAccountID != systemAccountID {
		return false, nil
	}
	delete(s.groupsByID, groupID)
	for i, id := range s.groupOrder {
		if id == groupID {
			s.groupOrder = append(s.groupOrder[:i], s.groupOrder[i+1:]...)
			break
		}
	}
	return true, nil
}

func (s *fakePublicGroupStore) PublicGroupAccountCount(_ context.Context, groupID string) (int64, error) {
	s.accountCountCalls = append(s.accountCountCalls, groupID)
	return s.accountCounts[groupID], nil
}

func (s *fakePublicGroupStore) PublicGroupActiveRouteStrategyLossCount(_ context.Context, groupID string) (int64, error) {
	s.routeLossCountCalls = append(s.routeLossCountCalls, groupID)
	return s.routeLossCounts[groupID], nil
}

func publicGroupsTestStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func publicGroupsTestContainsFold(value string, substr string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(substr))
}
