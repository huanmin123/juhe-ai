package publicroutestrategies

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

var publicRouteStrategiesTestNow = time.Date(2026, 7, 7, 11, 0, 0, 0, time.UTC)

func TestAddRequiresExistingTargetAndCreatesRouteStrategy(t *testing.T) {
	store := newFakePublicRouteStrategyStore()
	service, _ := newPublicRouteStrategiesTestService(store)

	_, err := service.Add(context.Background(), AddInput{
		TargetUsername: "alice",
		Name:           "公开策略",
		GroupBindings:  []GroupBindingInput{{GroupID: "grp_1"}},
	})
	if !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("Add() error = %v, want ErrTargetNotFound", err)
	}
	if len(store.createInputs) != 0 {
		t.Fatalf("CreatePublicRouteStrategy calls = %d, want 0", len(store.createInputs))
	}

	store.putTarget(publicRouteStrategyTarget("sys_1", "alice", "Alice", "active"))
	store.putGroup(port.PublicRouteStrategyBindableGroup{ID: "grp_1", SystemAccountID: "sys_1", Name: "主分组", ProviderCode: "gpt", Enabled: true})

	resp, err := service.Add(context.Background(), AddInput{
		TargetUsername: "alice",
		Name:           "公开策略",
		GroupBindings:  []GroupBindingInput{{GroupID: "grp_1"}},
	})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if resp.Action != "created" || resp.RouteStrategy == nil || resp.RouteStrategy.ID != "rts_test_1" {
		t.Fatalf("response = %+v", resp)
	}
	if resp.RouteStrategy.NormalRoutingConfig == nil || resp.RouteStrategy.NormalRoutingConfig.SchedulingPreference != defaultSchedulingPreference {
		t.Fatalf("normal config = %+v", resp.RouteStrategy.NormalRoutingConfig)
	}
	if got, want := len(store.createInputs), 1; got != want {
		t.Fatalf("create inputs = %d, want %d", got, want)
	}
	input := store.createInputs[0]
	if input.SystemAccountID != "sys_1" || input.Name != "公开策略" || input.Mode != port.PublicRouteStrategyModeNormal || input.Status != port.PublicRouteStrategyStatusActive {
		t.Fatalf("create input = %+v", input)
	}
	if input.ConfigJSON != nil {
		t.Fatalf("ConfigJSON = %q, want nil default config", *input.ConfigJSON)
	}
	if got, want := len(input.Bindings), 1; got != want || input.Bindings[0].ID != "rsg_test_2" || input.Bindings[0].Weight != 1 {
		t.Fatalf("bindings = %+v", input.Bindings)
	}
}

func TestAddAllowsAuthorizedCrossOwnerGroupAndReturnsBindingSummary(t *testing.T) {
	store := newFakePublicRouteStrategyStore()
	store.putTarget(publicRouteStrategyTarget("sys_1", "alice", "Alice", "active"))
	store.putTarget(publicRouteStrategyTarget("sys_2", "owner", "Owner", "active"))
	store.putGroup(port.PublicRouteStrategyBindableGroup{
		ID:              "grp_shared",
		SystemAccountID: "sys_2",
		Name:            "授权分组",
		ProviderCode:    "gpt",
		Enabled:         true,
	})
	service, _ := newPublicRouteStrategiesTestService(store)

	_, err := service.Add(context.Background(), AddInput{
		TargetUsername: "alice",
		Name:           "无授权策略",
		GroupBindings:  []GroupBindingInput{{GroupID: "grp_shared"}},
	})
	if !errors.Is(err, ErrGroupBoundary) {
		t.Fatalf("Add() ungranted cross-owner error = %v, want ErrGroupBoundary", err)
	}

	store.authorizeGroup("sys_1", "grp_shared", true)
	resp, err := service.Add(context.Background(), AddInput{
		TargetUsername: "alice",
		Name:           "授权策略",
		GroupBindings:  []GroupBindingInput{{GroupID: "grp_shared"}},
	})
	if err != nil {
		t.Fatalf("Add() authorized cross-owner error = %v", err)
	}
	if resp.RouteStrategy == nil || len(resp.RouteStrategy.GroupBindings) != 1 {
		t.Fatalf("response = %+v", resp)
	}
	binding := resp.RouteStrategy.GroupBindings[0]
	if binding.GroupID != "grp_shared" || binding.GroupName != "授权分组" || binding.ProviderCode != "gpt" || !binding.GroupEnabled {
		t.Fatalf("binding summary = %+v", binding)
	}
}

func TestAddRejectsDuplicateNameNonIdempotently(t *testing.T) {
	store := newFakePublicRouteStrategyStore()
	store.putTarget(publicRouteStrategyTarget("sys_1", "alice", "Alice", "active"))
	store.putGroup(port.PublicRouteStrategyBindableGroup{ID: "grp_1", SystemAccountID: "sys_1", Name: "主分组", ProviderCode: "gpt", Enabled: true})
	store.putRoute(port.PublicRouteStrategySummary{ID: "rts_existing", SystemAccountID: "sys_1", Name: "公开策略", Mode: port.PublicRouteStrategyModeNormal, Status: port.PublicRouteStrategyStatusActive})
	service, _ := newPublicRouteStrategiesTestService(store)

	_, err := service.Add(context.Background(), AddInput{
		TargetUsername: "alice",
		Name:           "公开策略",
		GroupBindings:  []GroupBindingInput{{GroupID: "grp_1"}},
	})

	if !errors.Is(err, ErrDuplicateRouteStrategyName) {
		t.Fatalf("Add() error = %v, want ErrDuplicateRouteStrategyName", err)
	}
	if got, want := len(store.createInputs), 1; got != want {
		t.Fatalf("CreatePublicRouteStrategy calls = %d, want %d duplicate attempt", got, want)
	}
}

func TestUpdateGuardsOwnerAndReplacesBindings(t *testing.T) {
	store := newFakePublicRouteStrategyStore()
	store.putTarget(publicRouteStrategyTarget("sys_1", "alice", "Alice", "active"))
	store.putTarget(publicRouteStrategyTarget("sys_2", "other", "Other", "active"))
	store.putGroup(port.PublicRouteStrategyBindableGroup{ID: "grp_1", SystemAccountID: "sys_1", Name: "主分组", ProviderCode: "gpt", Enabled: true})
	store.putGroup(port.PublicRouteStrategyBindableGroup{ID: "grp_2", SystemAccountID: "sys_1", Name: "备用", ProviderCode: "gpt", Enabled: true})
	store.putRoute(port.PublicRouteStrategySummary{
		ID:              "rts_1",
		SystemAccountID: "sys_1",
		Name:            "公开策略",
		Mode:            port.PublicRouteStrategyModeNormal,
		Status:          port.PublicRouteStrategyStatusActive,
		GroupBindings: []port.PublicRouteStrategyGroupBindingSummary{{
			ID: "rsg_1", GroupID: "grp_1", GroupName: "主分组", ProviderCode: "gpt", Priority: 1, Weight: 1, Status: port.PublicRouteStrategyStatusActive, GroupEnabled: true,
		}},
		CreatedAt: publicRouteStrategiesTestNow,
		UpdatedAt: publicRouteStrategiesTestNow,
	})
	service, _ := newPublicRouteStrategiesTestService(store)

	other := "other"
	if _, err := service.Update(context.Background(), UpdateInput{
		TargetUsername:  &other,
		RouteStrategyID: "rts_1",
		Name:            stringPtr("越权"),
	}); !errors.Is(err, ErrRouteStrategyNotFound) {
		t.Fatalf("Update() owner mismatch error = %v, want ErrRouteStrategyNotFound", err)
	}
	if len(store.updateInputs) != 0 {
		t.Fatalf("UpdatePublicRouteStrategy calls = %d, want 0", len(store.updateInputs))
	}

	mode := ModeFailover
	resp, err := service.Update(context.Background(), UpdateInput{
		RouteStrategyID: "rts_1",
		Mode:            &mode,
		GroupBindings: NewOptionalGroupBindings([]GroupBindingInput{
			{GroupID: "grp_2", Priority: 2},
			{GroupID: "grp_1", Priority: 1},
		}, true),
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if resp.Action != "updated" || resp.RouteStrategy == nil || resp.RouteStrategy.Mode != ModeFailover {
		t.Fatalf("response = %+v", resp)
	}
	input := store.updateInputs[0]
	if input.Mode != port.PublicRouteStrategyModeFailover || len(input.Bindings) != 2 || input.Bindings[0].GroupID != "grp_1" || input.Bindings[1].GroupID != "grp_2" {
		t.Fatalf("update input = %+v", input)
	}
}

func TestUpdateAllowsAuthorizedCrossOwnerGroupAndHonorsEffectiveEnabled(t *testing.T) {
	store := newFakePublicRouteStrategyStore()
	store.putTarget(publicRouteStrategyTarget("sys_1", "alice", "Alice", "active"))
	store.putTarget(publicRouteStrategyTarget("sys_2", "owner", "Owner", "active"))
	store.putGroup(port.PublicRouteStrategyBindableGroup{ID: "grp_own", SystemAccountID: "sys_1", Name: "自有分组", ProviderCode: "gpt", Enabled: true})
	store.putGroup(port.PublicRouteStrategyBindableGroup{ID: "grp_shared", SystemAccountID: "sys_2", Name: "授权分组", ProviderCode: "gpt", Enabled: true})
	store.putRoute(port.PublicRouteStrategySummary{
		ID:              "rts_1",
		SystemAccountID: "sys_1",
		Name:            "公开策略",
		Mode:            port.PublicRouteStrategyModeNormal,
		Status:          port.PublicRouteStrategyStatusActive,
		GroupBindings: []port.PublicRouteStrategyGroupBindingSummary{{
			ID: "rsg_1", GroupID: "grp_own", GroupName: "自有分组", ProviderCode: "gpt", Priority: 1, Weight: 1, Status: port.PublicRouteStrategyStatusActive, GroupEnabled: true,
		}},
	})
	service, _ := newPublicRouteStrategiesTestService(store)

	store.authorizeGroup("sys_1", "grp_shared", false)
	_, err := service.Update(context.Background(), UpdateInput{
		RouteStrategyID: "rts_1",
		GroupBindings: NewOptionalGroupBindings([]GroupBindingInput{{
			GroupID: "grp_shared",
		}}, true),
	})
	if !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("Update() disabled authorization error = %v, want ErrInvalidBinding", err)
	}

	store.authorizeGroup("sys_1", "grp_shared", true)
	resp, err := service.Update(context.Background(), UpdateInput{
		RouteStrategyID: "rts_1",
		GroupBindings: NewOptionalGroupBindings([]GroupBindingInput{{
			GroupID: "grp_shared",
		}}, true),
	})
	if err != nil {
		t.Fatalf("Update() authorized cross-owner error = %v", err)
	}
	if resp.RouteStrategy == nil || len(resp.RouteStrategy.GroupBindings) != 1 {
		t.Fatalf("response = %+v", resp)
	}
	binding := resp.RouteStrategy.GroupBindings[0]
	if binding.GroupID != "grp_shared" || binding.GroupName != "授权分组" || !binding.GroupEnabled {
		t.Fatalf("binding summary = %+v", binding)
	}
}

func TestUpdateWithoutGroupBindingsPreservesCurrentInvalidBinding(t *testing.T) {
	store := newFakePublicRouteStrategyStore()
	store.putTarget(publicRouteStrategyTarget("sys_1", "alice", "Alice", "active"))
	store.putGroup(port.PublicRouteStrategyBindableGroup{ID: "grp_shared", SystemAccountID: "sys_2", Name: "授权分组", ProviderCode: "gpt", Enabled: true})
	store.putRoute(port.PublicRouteStrategySummary{
		ID:              "rts_1",
		SystemAccountID: "sys_1",
		Name:            "公开策略",
		Mode:            port.PublicRouteStrategyModeNormal,
		Status:          port.PublicRouteStrategyStatusActive,
		GroupBindings: []port.PublicRouteStrategyGroupBindingSummary{{
			ID: "rsg_1", GroupID: "grp_shared", GroupName: "授权分组", ProviderCode: "gpt", Priority: 1, Weight: 25, Status: port.PublicRouteStrategyStatusActive, GroupEnabled: false,
		}},
	})
	service, _ := newPublicRouteStrategiesTestService(store)
	nextName := "局部更新策略"

	resp, err := service.Update(context.Background(), UpdateInput{
		RouteStrategyID: "rts_1",
		Name:            &nextName,
	})
	if err != nil {
		t.Fatalf("Update() partial update error = %v", err)
	}
	if resp.RouteStrategy == nil || resp.RouteStrategy.Name != nextName {
		t.Fatalf("response = %+v", resp)
	}
	if got := store.findBindableGroupsCalls; got != 0 {
		t.Fatalf("FindPublicRouteStrategyBindableGroups calls = %d, want 0", got)
	}
	if got := len(store.updateInputs); got != 1 {
		t.Fatalf("UpdatePublicRouteStrategy calls = %d, want 1", got)
	}
	bindings := store.updateInputs[0].Bindings
	if len(bindings) != 1 || bindings[0].GroupID != "grp_shared" || bindings[0].Priority != 1 || bindings[0].Weight != 25 || bindings[0].Status != port.PublicRouteStrategyStatusActive {
		t.Fatalf("preserved bindings = %+v", bindings)
	}
}

func TestUpdateWithoutGroupBindingsStillValidatesNextMode(t *testing.T) {
	store := newFakePublicRouteStrategyStore()
	store.putTarget(publicRouteStrategyTarget("sys_1", "alice", "Alice", "active"))
	store.putRoute(port.PublicRouteStrategySummary{
		ID:              "rts_1",
		SystemAccountID: "sys_1",
		Name:            "公开策略",
		Mode:            port.PublicRouteStrategyModeNormal,
		Status:          port.PublicRouteStrategyStatusActive,
		GroupBindings: []port.PublicRouteStrategyGroupBindingSummary{{
			ID: "rsg_1", GroupID: "grp_missing", GroupName: "已失效分组", ProviderCode: "gpt", Priority: 1, Weight: 1, Status: port.PublicRouteStrategyStatusActive, GroupEnabled: false,
		}},
	})
	service, _ := newPublicRouteStrategiesTestService(store)
	nextMode := ModeFailover

	_, err := service.Update(context.Background(), UpdateInput{
		RouteStrategyID: "rts_1",
		Mode:            &nextMode,
	})
	if !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("Update() mode validation error = %v, want ErrInvalidBinding", err)
	}
	if got := store.findBindableGroupsCalls; got != 0 {
		t.Fatalf("FindPublicRouteStrategyBindableGroups calls = %d, want 0", got)
	}
	if len(store.updateInputs) != 0 {
		t.Fatalf("UpdatePublicRouteStrategy calls = %d, want 0", len(store.updateInputs))
	}
}

func TestUpdateRejectsInvalidBindings(t *testing.T) {
	store := newFakePublicRouteStrategyStore()
	store.putTarget(publicRouteStrategyTarget("sys_1", "alice", "Alice", "active"))
	store.putGroup(port.PublicRouteStrategyBindableGroup{ID: "grp_1", SystemAccountID: "sys_1", Name: "主分组", ProviderCode: "gpt", Enabled: true})
	store.putGroup(port.PublicRouteStrategyBindableGroup{ID: "grp_2", SystemAccountID: "sys_1", Name: "停用分组", ProviderCode: "gpt", Enabled: false})
	store.putRoute(port.PublicRouteStrategySummary{
		ID:              "rts_1",
		SystemAccountID: "sys_1",
		Name:            "公开策略",
		Mode:            port.PublicRouteStrategyModeNormal,
		Status:          port.PublicRouteStrategyStatusActive,
		GroupBindings: []port.PublicRouteStrategyGroupBindingSummary{{
			ID: "rsg_1", GroupID: "grp_1", GroupName: "主分组", ProviderCode: "gpt", Priority: 1, Weight: 1, Status: port.PublicRouteStrategyStatusActive, GroupEnabled: true,
		}},
	})
	service, _ := newPublicRouteStrategiesTestService(store)

	_, err := service.Update(context.Background(), UpdateInput{
		RouteStrategyID: "rts_1",
		GroupBindings: NewOptionalGroupBindings([]GroupBindingInput{
			{GroupID: "grp_1", Priority: 1},
			{GroupID: "grp_2", Priority: 2},
		}, true),
	})
	if !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("Update() normal multi binding error = %v, want ErrInvalidBinding", err)
	}

	mode := ModeWeighted
	_, err = service.Update(context.Background(), UpdateInput{
		RouteStrategyID: "rts_1",
		Mode:            &mode,
		GroupBindings: NewOptionalGroupBindings([]GroupBindingInput{
			{GroupID: "grp_2", Priority: 1, Status: StatusActive},
		}, true),
	})
	if !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("Update() disabled active group error = %v, want ErrInvalidBinding", err)
	}
}

func TestDeleteRejectsDefaultAndAPIKeyUsage(t *testing.T) {
	store := newFakePublicRouteStrategyStore()
	store.putTarget(publicRouteStrategyTarget("sys_1", "alice", "Alice", "active"))
	store.putRoute(port.PublicRouteStrategySummary{ID: "rts_default", SystemAccountID: "sys_1", Name: "默认", Mode: port.PublicRouteStrategyModeNormal, Status: port.PublicRouteStrategyStatusActive, IsDefault: true})
	store.putRoute(port.PublicRouteStrategySummary{ID: "rts_used", SystemAccountID: "sys_1", Name: "使用中", Mode: port.PublicRouteStrategyModeNormal, Status: port.PublicRouteStrategyStatusActive})
	store.apiKeyCounts["rts_used"] = 2
	service, _ := newPublicRouteStrategiesTestService(store)

	if _, err := service.Delete(context.Background(), DeleteInput{RouteStrategyID: "rts_default"}); !errors.Is(err, ErrDefaultRouteStrategyDelete) {
		t.Fatalf("Delete() default error = %v, want ErrDefaultRouteStrategyDelete", err)
	}
	if _, err := service.Delete(context.Background(), DeleteInput{RouteStrategyID: "rts_used"}); !errors.Is(err, ErrRouteStrategyAPIKeysInUse) {
		t.Fatalf("Delete() api key usage error = %v, want ErrRouteStrategyAPIKeysInUse", err)
	}
	if len(store.deleteCalls) != 0 {
		t.Fatalf("DeletePublicRouteStrategy calls = %d, want 0", len(store.deleteCalls))
	}
}

func newPublicRouteStrategiesTestService(store *fakePublicRouteStrategyStore) (*Service, *fakePublicRouteStrategyTransactor) {
	tx := &fakePublicRouteStrategyTransactor{store: store}
	seq := 0
	service := NewService(Options{
		Store:      store,
		Transactor: tx,
		Now:        func() time.Time { return publicRouteStrategiesTestNow },
		NewID: func(prefix string) string {
			seq++
			return prefix + "_test_" + strconv.Itoa(seq)
		},
	})
	return service, tx
}

func publicRouteStrategyTarget(id string, username string, displayName string, status string) port.PublicGroupTarget {
	return port.PublicGroupTarget{ID: id, Username: username, DisplayName: displayName, Status: status}
}

type fakePublicRouteStrategyTransactor struct {
	store *fakePublicRouteStrategyStore
	calls int
}

func (t *fakePublicRouteStrategyTransactor) PublicRouteStrategyInTx(ctx context.Context, fn func(context.Context, port.PublicRouteStrategyStore) error) error {
	t.calls++
	return fn(ctx, t.store)
}

type fakePublicRouteStrategyStore struct {
	targetsByUsername   map[string]port.PublicGroupTarget
	targetsByID         map[string]port.PublicGroupTarget
	groupsByID          map[string]port.PublicRouteStrategyBindableGroup
	groupAuthorizations map[string]map[string]bool
	routesByID          map[string]port.PublicRouteStrategySummary
	routeOrder          []string
	apiKeyCounts        map[string]int64

	createInputs []port.PublicRouteStrategyCreateInput
	updateInputs []port.PublicRouteStrategyUpdateInput
	deleteCalls  []string

	findBindableGroupsCalls int
}

func newFakePublicRouteStrategyStore() *fakePublicRouteStrategyStore {
	return &fakePublicRouteStrategyStore{
		targetsByUsername:   map[string]port.PublicGroupTarget{},
		targetsByID:         map[string]port.PublicGroupTarget{},
		groupsByID:          map[string]port.PublicRouteStrategyBindableGroup{},
		groupAuthorizations: map[string]map[string]bool{},
		routesByID:          map[string]port.PublicRouteStrategySummary{},
		apiKeyCounts:        map[string]int64{},
	}
}

func (s *fakePublicRouteStrategyStore) putTarget(target port.PublicGroupTarget) {
	s.targetsByUsername[target.Username] = target
	s.targetsByID[target.ID] = target
}

func (s *fakePublicRouteStrategyStore) putGroup(group port.PublicRouteStrategyBindableGroup) {
	s.groupsByID[group.ID] = group
}

func (s *fakePublicRouteStrategyStore) authorizeGroup(systemAccountID string, groupID string, enabled bool) {
	if s.groupAuthorizations[systemAccountID] == nil {
		s.groupAuthorizations[systemAccountID] = map[string]bool{}
	}
	s.groupAuthorizations[systemAccountID][groupID] = enabled
}

func (s *fakePublicRouteStrategyStore) putRoute(route port.PublicRouteStrategySummary) {
	if route.CreatedAt.IsZero() {
		route.CreatedAt = publicRouteStrategiesTestNow
	}
	if route.UpdatedAt.IsZero() {
		route.UpdatedAt = publicRouteStrategiesTestNow
	}
	if _, exists := s.routesByID[route.ID]; !exists {
		s.routeOrder = append(s.routeOrder, route.ID)
	}
	s.routesByID[route.ID] = route
}

func (s *fakePublicRouteStrategyStore) FindPublicRouteStrategyTargetByUsername(_ context.Context, username string) (port.PublicGroupTarget, bool, error) {
	target, ok := s.targetsByUsername[strings.TrimSpace(username)]
	return target, ok, nil
}

func (s *fakePublicRouteStrategyStore) FindPublicRouteStrategyTargetByID(_ context.Context, id string) (port.PublicGroupTarget, bool, error) {
	target, ok := s.targetsByID[id]
	return target, ok, nil
}

func (s *fakePublicRouteStrategyStore) ListPublicRouteStrategies(_ context.Context, input port.PublicRouteStrategyListInput) (port.PublicRouteStrategyListPage, error) {
	page := input.Page
	if page <= 0 {
		page = 1
	}
	pageSize := input.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	items := make([]port.PublicRouteStrategySummary, 0, len(s.routeOrder))
	for _, id := range s.routeOrder {
		route := s.routesByID[id]
		if route.SystemAccountID != input.SystemAccountID {
			continue
		}
		if input.Keyword != "" && !strings.HasPrefix(strings.ToLower(route.Name), strings.ToLower(input.Keyword)) {
			continue
		}
		if input.Mode != "" && string(route.Mode) != input.Mode {
			continue
		}
		if input.Status != "" && string(route.Status) != input.Status {
			continue
		}
		items = append(items, route)
	}
	upper := 0
	if len(items) > 0 {
		upper = (len(items) + pageSize - 1) / pageSize
	}
	start := (page - 1) * pageSize
	if start > len(items) {
		start = len(items)
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return port.PublicRouteStrategyListPage{Items: append([]port.PublicRouteStrategySummary(nil), items[start:end]...), Page: page, PageSize: pageSize, PageUpperBound: upper, HasMore: page < upper}, nil
}

func (s *fakePublicRouteStrategyStore) FindPublicRouteStrategyByID(_ context.Context, routeStrategyID string) (port.PublicRouteStrategySummary, bool, error) {
	route, ok := s.routesByID[routeStrategyID]
	return route, ok, nil
}

func (s *fakePublicRouteStrategyStore) FindPublicRouteStrategyBindableGroups(_ context.Context, systemAccountID string, groupIDs []string) ([]port.PublicRouteStrategyBindableGroup, error) {
	s.findBindableGroupsCalls++
	out := make([]port.PublicRouteStrategyBindableGroup, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		group, ok := s.bindableGroup(systemAccountID, groupID)
		if ok {
			out = append(out, group)
		}
	}
	return out, nil
}

func (s *fakePublicRouteStrategyStore) CreatePublicRouteStrategy(_ context.Context, input port.PublicRouteStrategyCreateInput) (port.PublicRouteStrategySummary, error) {
	s.createInputs = append(s.createInputs, input)
	if s.routeNameExists(input.SystemAccountID, input.Name, "") {
		return port.PublicRouteStrategySummary{}, port.ErrPublicRouteStrategyDuplicateName
	}
	route := port.PublicRouteStrategySummary{
		ID:              input.ID,
		SystemAccountID: input.SystemAccountID,
		Name:            input.Name,
		Description:     input.Description,
		Mode:            input.Mode,
		Status:          input.Status,
		ConfigJSON:      input.ConfigJSON,
		GroupBindings:   s.bindingSummaries(input.SystemAccountID, input.Bindings),
		APIKeyCount:     0,
		CreatedAt:       input.Now,
		UpdatedAt:       input.Now,
	}
	s.putRoute(route)
	return route, nil
}

func (s *fakePublicRouteStrategyStore) UpdatePublicRouteStrategy(_ context.Context, input port.PublicRouteStrategyUpdateInput) (port.PublicRouteStrategySummary, bool, error) {
	s.updateInputs = append(s.updateInputs, input)
	current, ok := s.routesByID[input.ID]
	if !ok || current.SystemAccountID != input.SystemAccountID {
		return port.PublicRouteStrategySummary{}, false, nil
	}
	if s.routeNameExists(input.SystemAccountID, input.Name, input.ID) {
		return port.PublicRouteStrategySummary{}, false, port.ErrPublicRouteStrategyDuplicateName
	}
	current.Name = input.Name
	current.Description = input.Description
	current.Mode = input.Mode
	current.Status = input.Status
	current.ConfigJSON = input.ConfigJSON
	current.GroupBindings = s.bindingSummaries(input.SystemAccountID, input.Bindings)
	current.UpdatedAt = input.Now
	s.routesByID[input.ID] = current
	return current, true, nil
}

func (s *fakePublicRouteStrategyStore) DeletePublicRouteStrategy(_ context.Context, routeStrategyID string, systemAccountID string) (bool, error) {
	s.deleteCalls = append(s.deleteCalls, routeStrategyID)
	route, ok := s.routesByID[routeStrategyID]
	if !ok || route.SystemAccountID != systemAccountID {
		return false, nil
	}
	delete(s.routesByID, routeStrategyID)
	return true, nil
}

func (s *fakePublicRouteStrategyStore) PublicRouteStrategyAPIKeyCount(_ context.Context, routeStrategyID string, _ string) (int64, error) {
	return s.apiKeyCounts[routeStrategyID], nil
}

func (s *fakePublicRouteStrategyStore) routeNameExists(systemAccountID string, name string, exceptID string) bool {
	for _, route := range s.routesByID {
		if route.ID != exceptID && route.SystemAccountID == systemAccountID && strings.EqualFold(route.Name, name) {
			return true
		}
	}
	return false
}

func (s *fakePublicRouteStrategyStore) bindableGroup(systemAccountID string, groupID string) (port.PublicRouteStrategyBindableGroup, bool) {
	group, ok := s.groupsByID[groupID]
	if !ok {
		return port.PublicRouteStrategyBindableGroup{}, false
	}
	if group.SystemAccountID == systemAccountID {
		return group, true
	}
	enabled, ok := s.groupAuthorizations[systemAccountID][groupID]
	if !ok {
		return port.PublicRouteStrategyBindableGroup{}, false
	}
	group.Enabled = group.Enabled && enabled
	return group, true
}

func (s *fakePublicRouteStrategyStore) bindingSummaries(systemAccountID string, bindings []port.PublicRouteStrategyGroupBindingCreateInput) []port.PublicRouteStrategyGroupBindingSummary {
	out := make([]port.PublicRouteStrategyGroupBindingSummary, 0, len(bindings))
	for _, binding := range bindings {
		group, _ := s.bindableGroup(systemAccountID, binding.GroupID)
		out = append(out, port.PublicRouteStrategyGroupBindingSummary{
			ID:           binding.ID,
			GroupID:      binding.GroupID,
			GroupName:    group.Name,
			ProviderCode: group.ProviderCode,
			Priority:     binding.Priority,
			Weight:       binding.Weight,
			Status:       binding.Status,
			GroupEnabled: group.Enabled,
		})
	}
	return out
}

func stringPtr(value string) *string {
	return &value
}
