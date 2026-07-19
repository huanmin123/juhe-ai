package managementroutestrategies

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceDeleteCommitsAtomicDeleteAndReturnsSafeBeforeSummary(t *testing.T) {
	events := []string{}
	store := newManagementRouteStrategyDeleteStore()
	store.events = &events
	tx := &managementRouteStrategyDeleteTransactor{
		store:  store,
		events: &events,
	}
	invalidator := &managementRouteStrategyDeleteInvalidator{events: &events}
	service := NewServiceWithOptions(ServiceOptions{
		CreateStore: store,
		Transactor:  tx,
		Invalidator: invalidator,
	})
	publisher := &routeStrategyPageDataPublisherStub{}
	service.pageDataPublisher = publisher

	result, err := service.Delete(context.Background(), DeleteInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		RouteStrategyID:      " route_1 ",
	})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if !result.Committed ||
		result.OwnerSystemAccountID != "sys_owner" ||
		result.Before.ID != "route_1" ||
		result.Before.Name != "生产策略" ||
		result.Before.Mode != "normal" ||
		result.Before.Status != "active" {
		t.Fatalf("Delete() result = %+v", result)
	}
	encodedBefore, err := json.Marshal(result.Before)
	if err != nil {
		t.Fatalf("marshal before summary: %v", err)
	}
	var beforeObject map[string]json.RawMessage
	if err := json.Unmarshal(encodedBefore, &beforeObject); err != nil {
		t.Fatalf("unmarshal before summary object: %v", err)
	}
	beforeKeys := make([]string, 0, len(beforeObject))
	for key := range beforeObject {
		beforeKeys = append(beforeKeys, key)
	}
	sort.Strings(beforeKeys)
	if got, want := beforeKeys, []string{"id", "mode", "name", "status"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("before summary keys = %#v, want exactly %#v: %s", got, want, encodedBefore)
	}
	if got, want := events, []string{
		"tx:begin",
		"find:lock",
		"count",
		"delete",
		"tx:commit",
		"invalidate",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	if store.findCalls != 1 ||
		store.countCalls != 1 ||
		store.deleteCalls != 1 ||
		store.deleteRouteStrategyID != "route_1" ||
		store.deleteSystemAccountID != "sys_owner" ||
		!store.allMutationCallsWereInTx {
		t.Fatalf("store = %+v", store)
	}
	if tx.calls != 1 || tx.commits != 1 || tx.rollbacks != 0 {
		t.Fatalf("transaction = %+v", tx)
	}
	if invalidator.calls != 1 ||
		invalidator.reason != RouteStrategyDeletedReason {
		t.Fatalf("invalidator = %+v", invalidator)
	}
	assertRouteStrategyPageDataReset(t, publisher)
}

func TestServiceDeleteUsesOnlyTransactionCallbackStore(t *testing.T) {
	normalStore := &managementRouteStrategyDeleteForbiddenStore{}
	txStore := newManagementRouteStrategyDeleteStore()
	tx := &managementRouteStrategyDeleteTransactor{store: txStore}
	service := NewServiceWithOptions(ServiceOptions{
		CreateStore: normalStore,
		Transactor:  tx,
	})

	result, err := service.Delete(context.Background(), DeleteInput{
		ActorSystemAccountID: "sys_owner",
		RouteStrategyID:      "route_1",
	})
	if err != nil || !result.Committed {
		t.Fatalf("Delete() result=%+v error=%v", result, err)
	}
	if len(normalStore.calls) != 0 {
		t.Fatalf("ordinary createStore calls = %#v, want none", normalStore.calls)
	}
	if txStore.lockedFindCalls != 1 ||
		txStore.countCalls != 1 ||
		txStore.deleteCalls != 1 ||
		!txStore.allMutationCallsWereInTx {
		t.Fatalf("transaction callback store = %+v", txStore)
	}
}

func TestProductionPublicRouteStrategyTxStoreFindByIDPassesForUpdateTrue(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve service delete test source path")
	}
	sourcePath := filepath.Clean(filepath.Join(
		filepath.Dir(testFile),
		"..",
		"..",
		"store",
		"postgres",
		"publicroutestrategies.go",
	))
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, sourcePath, nil, 0)
	if err != nil {
		t.Fatalf("parse production public route strategy store: %v", err)
	}

	var method *ast.FuncDecl
	for _, declaration := range parsed.Decls {
		candidate, ok := declaration.(*ast.FuncDecl)
		if !ok ||
			candidate.Name.Name != "FindPublicRouteStrategyByID" ||
			candidate.Recv == nil ||
			len(candidate.Recv.List) != 1 ||
			astReceiverTypeName(candidate.Recv.List[0].Type) != "publicRouteStrategyTxStore" {
			continue
		}
		method = candidate
		break
	}
	if method == nil {
		t.Fatal("production publicRouteStrategyTxStore.FindPublicRouteStrategyByID not found")
	}
	if method.Body == nil || len(method.Body.List) != 1 {
		t.Fatalf("tx store FindPublicRouteStrategyByID body changed: %#v", method.Body)
	}
	returnStatement, ok := method.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(returnStatement.Results) != 1 {
		t.Fatalf("tx store FindPublicRouteStrategyByID must directly return locked lookup")
	}
	call, ok := returnStatement.Results[0].(*ast.CallExpr)
	if !ok || len(call.Args) != 4 {
		t.Fatalf("tx store FindPublicRouteStrategyByID call changed: %#v", returnStatement.Results[0])
	}
	function, ok := call.Fun.(*ast.Ident)
	forUpdate, forUpdateOK := call.Args[3].(*ast.Ident)
	if !ok ||
		function.Name != "publicRouteStrategyFindByID" ||
		!forUpdateOK ||
		forUpdate.Name != "true" {
		t.Fatalf(
			"tx store FindPublicRouteStrategyByID must call publicRouteStrategyFindByID with forUpdate=true",
		)
	}
}

func TestProductionFindPublicRouteStrategyByIDForUpdateSQLSectionLocksRow(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve service delete test source path")
	}
	sourcePath := filepath.Clean(filepath.Join(
		filepath.Dir(testFile),
		"..",
		"..",
		"store",
		"postgres",
		"queries",
		"w1b_public_route_strategies.sql",
	))
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read production public route strategy SQL: %v", err)
	}

	unlockedSection, err := namedSQLQuerySection(
		string(source),
		"FindPublicRouteStrategyByID",
	)
	if err != nil {
		t.Fatalf("locate unlocked route strategy query: %v", err)
	}
	if sqlSectionHasIndependentForUpdate(unlockedSection) {
		t.Fatalf(
			"unlocked FindPublicRouteStrategyByID section unexpectedly contains FOR UPDATE:\n%s",
			unlockedSection,
		)
	}

	lockedSection, err := namedSQLQuerySection(
		string(source),
		"FindPublicRouteStrategyByIDForUpdate",
	)
	if err != nil {
		t.Fatalf("locate locked route strategy query: %v", err)
	}
	if !sqlSectionHasIndependentForUpdate(lockedSection) {
		t.Fatalf(
			"FindPublicRouteStrategyByIDForUpdate section must contain an independent FOR UPDATE clause:\n%s",
			lockedSection,
		)
	}
}

func TestServiceDeleteEnforcesAdminGlobalOwnerNarrowingAndSelfActorScopes(t *testing.T) {
	allowed := []struct {
		name         string
		input        DeleteInput
		currentOwner string
	}{
		{
			name: "admin global by blank owner",
			input: DeleteInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
			},
			currentOwner: "sys_owner",
		},
		{
			name: "super admin global by all",
			input: DeleteInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "super_admin",
				SystemAccountID:      " all ",
			},
			currentOwner: "sys_owner",
		},
		{
			name: "admin explicit owner narrows",
			input: DeleteInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
				SystemAccountID:      " sys_owner ",
			},
			currentOwner: "sys_owner",
		},
		{
			name: "self only forces actor despite admin role",
			input: DeleteInput{
				ActorSystemAccountID: " sys_self ",
				ActorRole:            "admin",
				SystemAccountID:      "sys_forged",
				SelfOnly:             true,
			},
			currentOwner: "sys_self",
		},
		{
			name: "non admin forces actor",
			input: DeleteInput{
				ActorSystemAccountID: " sys_user ",
				ActorRole:            "user",
				SystemAccountID:      "sys_forged",
			},
			currentOwner: "sys_user",
		},
	}
	for _, test := range allowed {
		t.Run(test.name, func(t *testing.T) {
			store := newManagementRouteStrategyDeleteStore()
			store.current.SystemAccountID = test.currentOwner
			tx := &managementRouteStrategyDeleteTransactor{store: store}
			service := NewServiceWithOptions(ServiceOptions{
				CreateStore: store,
				Transactor:  tx,
			})
			input := test.input
			input.RouteStrategyID = "route_1"

			result, err := service.Delete(context.Background(), input)
			if err != nil ||
				!result.Committed ||
				result.OwnerSystemAccountID != test.currentOwner {
				t.Fatalf("Delete() result=%+v error=%v", result, err)
			}
		})
	}

	denied := []struct {
		name  string
		input DeleteInput
	}{
		{
			name: "admin owner narrowing mismatch",
			input: DeleteInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
				SystemAccountID:      "sys_other",
			},
		},
		{
			name: "self only cannot delete another owner",
			input: DeleteInput{
				ActorSystemAccountID: "sys_self",
				ActorRole:            "admin",
				SelfOnly:             true,
			},
		},
		{
			name: "non admin cannot delete another owner",
			input: DeleteInput{
				ActorSystemAccountID: "sys_user",
				ActorRole:            "user",
			},
		},
	}
	for _, test := range denied {
		t.Run(test.name, func(t *testing.T) {
			store := newManagementRouteStrategyDeleteStore()
			tx := &managementRouteStrategyDeleteTransactor{store: store}
			invalidator := &managementRouteStrategyDeleteInvalidator{}
			service := NewServiceWithOptions(ServiceOptions{
				CreateStore: store,
				Transactor:  tx,
				Invalidator: invalidator,
			})
			input := test.input
			input.RouteStrategyID = "route_1"

			result, err := service.Delete(context.Background(), input)
			var notFound *NotFoundError
			if !errors.As(err, &notFound) ||
				notFound.RouteStrategyID != "route_1" {
				t.Fatalf("Delete() result=%+v error=%T %v, want typed not found", result, err, err)
			}
			message, ok := NotFoundMessage(err)
			if !ok || message != "策略路由不存在" {
				t.Fatalf("not found message = %q, %v", message, ok)
			}
			if result.Committed ||
				store.countCalls != 0 ||
				store.deleteCalls != 0 ||
				invalidator.calls != 0 {
				t.Fatalf(
					"result=%+v count=%d delete=%d invalidation=%d",
					result,
					store.countCalls,
					store.deleteCalls,
					invalidator.calls,
				)
			}
		})
	}
}

func TestServiceDeleteRejectsDefaultAndAPIKeyReferencesAsTypedConflicts(t *testing.T) {
	tests := []struct {
		name           string
		mutate         func(*managementRouteStrategyDeleteStore)
		wantKind       DeleteConflictKind
		wantCount      int64
		wantMessage    string
		wantCountCalls int
	}{
		{
			name: "default route strategy",
			mutate: func(store *managementRouteStrategyDeleteStore) {
				store.current.IsDefault = true
			},
			wantKind:    DeleteConflictDefault,
			wantMessage: "默认策略路由不允许删除",
		},
		{
			name: "api keys in use",
			mutate: func(store *managementRouteStrategyDeleteStore) {
				store.count = 3
			},
			wantKind:       DeleteConflictAPIKeysInUse,
			wantCount:      3,
			wantMessage:    "策略路由已被 3 个 API Key 使用，请先解绑",
			wantCountCalls: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newManagementRouteStrategyDeleteStore()
			test.mutate(store)
			tx := &managementRouteStrategyDeleteTransactor{store: store}
			invalidator := &managementRouteStrategyDeleteInvalidator{}
			service := NewServiceWithOptions(ServiceOptions{
				CreateStore: store,
				Transactor:  tx,
				Invalidator: invalidator,
			})

			result, err := service.Delete(context.Background(), DeleteInput{
				ActorSystemAccountID: "sys_owner",
				RouteStrategyID:      "route_1",
			})
			var conflict *DeleteConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("Delete() error = %T %v, want typed conflict", err, err)
			}
			if conflict.Kind != test.wantKind ||
				conflict.APIKeyCount != test.wantCount ||
				err.Error() != test.wantMessage {
				t.Fatalf("Delete() conflict = %+v error=%q", conflict, err)
			}
			message, ok := DeleteConflictMessage(err)
			if !ok || message != test.wantMessage {
				t.Fatalf("conflict message = %q, %v", message, ok)
			}
			if result.Committed ||
				store.countCalls != test.wantCountCalls ||
				store.deleteCalls != 0 ||
				tx.commits != 0 ||
				tx.rollbacks != 1 ||
				invalidator.calls != 0 {
				t.Fatalf(
					"result=%+v count=%d delete=%d tx=%+v invalidator=%+v",
					result,
					store.countCalls,
					store.deleteCalls,
					tx,
					invalidator,
				)
			}
		})
	}
}

func TestServiceDeleteCommitFailureReturnsUncommittedAndDoesNotInvalidate(t *testing.T) {
	events := []string{}
	store := newManagementRouteStrategyDeleteStore()
	store.events = &events
	commitErr := errors.New("commit failed")
	tx := &managementRouteStrategyDeleteTransactor{
		store:    store,
		events:   &events,
		afterErr: commitErr,
	}
	invalidator := &managementRouteStrategyDeleteInvalidator{events: &events}
	service := NewServiceWithOptions(ServiceOptions{
		CreateStore: store,
		Transactor:  tx,
		Invalidator: invalidator,
	})

	result, err := service.Delete(context.Background(), DeleteInput{
		ActorSystemAccountID: "sys_owner",
		RouteStrategyID:      "route_1",
	})
	if !errors.Is(err, commitErr) {
		t.Fatalf("Delete() error = %v, want %v", err, commitErr)
	}
	if !reflect.DeepEqual(result, DeleteResult{}) {
		t.Fatalf("Delete() result = %+v, want uncommitted zero result", result)
	}
	if got, want := events, []string{
		"tx:begin",
		"find:lock",
		"count",
		"delete",
		"tx:commit-failed",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	if tx.commits != 0 ||
		tx.rollbacks != 1 ||
		invalidator.calls != 0 {
		t.Fatalf("transaction=%+v invalidator=%+v", tx, invalidator)
	}
}

func TestServiceDeleteInvalidationIsDetachedBoundedAndBestEffort(t *testing.T) {
	events := []string{}
	parent, cancel := context.WithCancel(context.Background())
	store := newManagementRouteStrategyDeleteStore()
	store.events = &events
	tx := &managementRouteStrategyDeleteTransactor{
		store:       store,
		events:      &events,
		afterCommit: cancel,
	}
	invalidator := &managementRouteStrategyDeleteInvalidator{
		events: &events,
		err:    errors.New("invalidation unavailable"),
	}
	var logs bytes.Buffer
	service := NewServiceWithOptions(ServiceOptions{
		CreateStore: store,
		Transactor:  tx,
		Invalidator: invalidator,
		Logger:      slog.New(slog.NewTextHandler(&logs, nil)),
	})

	startedAt := time.Now()
	result, err := service.Delete(parent, DeleteInput{
		ActorSystemAccountID: "sys_owner",
		RouteStrategyID:      "route_1",
	})
	if err != nil || !result.Committed {
		t.Fatalf("Delete() result=%+v error=%v", result, err)
	}
	if !errors.Is(parent.Err(), context.Canceled) {
		t.Fatalf("parent context error = %v, want canceled", parent.Err())
	}
	if invalidator.calls != 1 ||
		invalidator.reason != RouteStrategyDeletedReason ||
		invalidator.contextErr != nil ||
		!invalidator.hasDeadline ||
		!invalidator.deadline.After(startedAt) ||
		invalidator.deadline.After(startedAt.Add(5*time.Second+250*time.Millisecond)) {
		t.Fatalf("invalidator = %+v", invalidator)
	}
	if got, want := events, []string{
		"tx:begin",
		"find:lock",
		"count",
		"delete",
		"tx:commit",
		"invalidate",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	logText := logs.String()
	if !strings.Contains(
		logText,
		"策略路由删除后网关运行态失效失败",
	) ||
		!strings.Contains(logText, RouteStrategyDeletedReason) {
		t.Fatalf("warning log = %q", logText)
	}
}

func TestServiceDeleteReturnsTypedNotFoundForMissingOrConcurrentDelete(t *testing.T) {
	tests := []struct {
		name            string
		mutate          func(*managementRouteStrategyDeleteStore)
		wantCountCalls  int
		wantDeleteCalls int
	}{
		{
			name: "missing",
			mutate: func(store *managementRouteStrategyDeleteStore) {
				store.found = false
			},
		},
		{
			name: "deleted after lock",
			mutate: func(store *managementRouteStrategyDeleteStore) {
				store.deleted = false
			},
			wantCountCalls:  1,
			wantDeleteCalls: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newManagementRouteStrategyDeleteStore()
			test.mutate(store)
			tx := &managementRouteStrategyDeleteTransactor{store: store}
			invalidator := &managementRouteStrategyDeleteInvalidator{}
			service := NewServiceWithOptions(ServiceOptions{
				CreateStore: store,
				Transactor:  tx,
				Invalidator: invalidator,
			})

			result, err := service.Delete(context.Background(), DeleteInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
				RouteStrategyID:      " route_1 ",
			})
			var notFound *NotFoundError
			if !errors.As(err, &notFound) ||
				notFound.RouteStrategyID != "route_1" ||
				err.Error() != "策略路由不存在" {
				t.Fatalf("Delete() result=%+v error=%T %v", result, err, err)
			}
			if result.Committed ||
				store.countCalls != test.wantCountCalls ||
				store.deleteCalls != test.wantDeleteCalls ||
				tx.commits != 0 ||
				tx.rollbacks != 1 ||
				invalidator.calls != 0 {
				t.Fatalf(
					"result=%+v store=%+v tx=%+v invalidator=%+v",
					result,
					store,
					tx,
					invalidator,
				)
			}
		})
	}
}

func TestServiceDeleteWrapsOperationalFailuresAsTypedInternal(t *testing.T) {
	findErr := errors.New("find unavailable")
	countErr := errors.New("count unavailable")
	deleteErr := errors.New("delete unavailable")
	commitErr := errors.New("commit unavailable")
	tests := []struct {
		name      string
		service   func(*managementRouteStrategyDeleteInvalidator) *Service
		wantCause error
	}{
		{
			name: "missing store",
			service: func(invalidator *managementRouteStrategyDeleteInvalidator) *Service {
				return NewServiceWithOptions(ServiceOptions{
					Transactor:  &managementRouteStrategyDeleteTransactor{},
					Invalidator: invalidator,
				})
			},
		},
		{
			name: "missing transactor",
			service: func(invalidator *managementRouteStrategyDeleteInvalidator) *Service {
				return NewServiceWithOptions(ServiceOptions{
					CreateStore: newManagementRouteStrategyDeleteStore(),
					Invalidator: invalidator,
				})
			},
		},
		{
			name: "find",
			service: func(invalidator *managementRouteStrategyDeleteInvalidator) *Service {
				store := newManagementRouteStrategyDeleteStore()
				store.currentErr = findErr
				return NewServiceWithOptions(ServiceOptions{
					CreateStore: store,
					Transactor: &managementRouteStrategyDeleteTransactor{
						store: store,
					},
					Invalidator: invalidator,
				})
			},
			wantCause: findErr,
		},
		{
			name: "api key count",
			service: func(invalidator *managementRouteStrategyDeleteInvalidator) *Service {
				store := newManagementRouteStrategyDeleteStore()
				store.countErr = countErr
				return NewServiceWithOptions(ServiceOptions{
					CreateStore: store,
					Transactor: &managementRouteStrategyDeleteTransactor{
						store: store,
					},
					Invalidator: invalidator,
				})
			},
			wantCause: countErr,
		},
		{
			name: "delete",
			service: func(invalidator *managementRouteStrategyDeleteInvalidator) *Service {
				store := newManagementRouteStrategyDeleteStore()
				store.deleteErr = deleteErr
				return NewServiceWithOptions(ServiceOptions{
					CreateStore: store,
					Transactor: &managementRouteStrategyDeleteTransactor{
						store: store,
					},
					Invalidator: invalidator,
				})
			},
			wantCause: deleteErr,
		},
		{
			name: "commit",
			service: func(invalidator *managementRouteStrategyDeleteInvalidator) *Service {
				store := newManagementRouteStrategyDeleteStore()
				return NewServiceWithOptions(ServiceOptions{
					CreateStore: store,
					Transactor: &managementRouteStrategyDeleteTransactor{
						store:    store,
						afterErr: commitErr,
					},
					Invalidator: invalidator,
				})
			},
			wantCause: commitErr,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalidator := &managementRouteStrategyDeleteInvalidator{}
			result, err := test.service(invalidator).Delete(
				context.Background(),
				DeleteInput{
					ActorSystemAccountID: "sys_owner",
					RouteStrategyID:      "route_1",
				},
			)
			var internalErr *DeleteInternalError
			if !errors.As(err, &internalErr) ||
				strings.TrimSpace(internalErr.Operation) == "" {
				t.Fatalf("Delete() error = %T %v, want typed internal", err, err)
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("Delete() error = %v, want cause %v", err, test.wantCause)
			}
			if result.Committed || invalidator.calls != 0 {
				t.Fatalf("result=%+v invalidator=%+v", result, invalidator)
			}
		})
	}
}

func TestServiceDeleteRejectsInvalidScopeBeforeTransaction(t *testing.T) {
	tests := []DeleteInput{
		{RouteStrategyID: "route_1"},
		{ActorSystemAccountID: "   ", RouteStrategyID: "route_1"},
		{ActorSystemAccountID: "sys_actor"},
		{ActorSystemAccountID: "sys_actor", RouteStrategyID: "   "},
	}
	for _, input := range tests {
		store := newManagementRouteStrategyDeleteStore()
		tx := &managementRouteStrategyDeleteTransactor{store: store}
		service := NewServiceWithOptions(ServiceOptions{
			CreateStore: store,
			Transactor:  tx,
		})

		_, err := service.Delete(context.Background(), input)
		message, ok := ValidationMessage(err)
		if !ok || message != "策略路由删除作用域无效" {
			t.Fatalf("Delete(%+v) error=%T %v message=%q", input, err, err, message)
		}
		if tx.calls != 0 || store.findCalls != 0 {
			t.Fatalf("Delete(%+v) tx=%+v store=%+v", input, tx, store)
		}
	}
}

type managementRouteStrategyDeleteStore struct {
	current port.PublicRouteStrategySummary
	found   bool

	currentErr error
	count      int64
	countErr   error
	deleted    bool
	deleteErr  error

	events                   *[]string
	findCalls                int
	lockedFindCalls          int
	countCalls               int
	deleteCalls              int
	deleteRouteStrategyID    string
	deleteSystemAccountID    string
	allMutationCallsWereInTx bool
}

func newManagementRouteStrategyDeleteStore() *managementRouteStrategyDeleteStore {
	description := "不应进入操作日志"
	configJSON := `{"normalRoutingConfig":{"schedulingPreference":"speed_first"}}`
	return &managementRouteStrategyDeleteStore{
		current: port.PublicRouteStrategySummary{
			ID:              "route_1",
			SystemAccountID: "sys_owner",
			Name:            "生产策略",
			Description:     &description,
			Mode:            port.PublicRouteStrategyModeNormal,
			Status:          port.PublicRouteStrategyStatusActive,
			ConfigJSON:      &configJSON,
			GroupBindings: []port.PublicRouteStrategyGroupBindingSummary{{
				ID:      "binding_1",
				GroupID: "group_1",
			}},
			APIKeyCount: 99,
			CreatedAt:   time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
			UpdatedAt:   time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC),
		},
		found:                    true,
		deleted:                  true,
		allMutationCallsWereInTx: true,
	}
}

func (s *managementRouteStrategyDeleteStore) FindPublicRouteStrategyTargetByUsername(
	context.Context,
	string,
) (port.PublicGroupTarget, bool, error) {
	return port.PublicGroupTarget{}, false, errors.New("unexpected username target lookup")
}

func (s *managementRouteStrategyDeleteStore) FindPublicRouteStrategyTargetByID(
	context.Context,
	string,
) (port.PublicGroupTarget, bool, error) {
	return port.PublicGroupTarget{}, false, errors.New("unexpected id target lookup")
}

func (s *managementRouteStrategyDeleteStore) ListPublicRouteStrategies(
	context.Context,
	port.PublicRouteStrategyListInput,
) (port.PublicRouteStrategyListPage, error) {
	return port.PublicRouteStrategyListPage{}, errors.New("unexpected route strategy list")
}

func (s *managementRouteStrategyDeleteStore) FindPublicRouteStrategyByID(
	ctx context.Context,
	routeStrategyID string,
) (port.PublicRouteStrategySummary, bool, error) {
	s.findCalls++
	if managementRouteStrategyDeleteInTx(ctx) {
		s.lockedFindCalls++
	} else {
		s.allMutationCallsWereInTx = false
	}
	managementRouteStrategyDeleteEvent(s.events, "find:lock")
	if s.currentErr != nil {
		return port.PublicRouteStrategySummary{}, false, s.currentErr
	}
	if !s.found || s.current.ID != routeStrategyID {
		return port.PublicRouteStrategySummary{}, false, nil
	}
	return s.current, true, nil
}

func (s *managementRouteStrategyDeleteStore) FindPublicRouteStrategyBindableGroups(
	context.Context,
	string,
	[]string,
) ([]port.PublicRouteStrategyBindableGroup, error) {
	return nil, errors.New("unexpected bindable group lookup")
}

func (s *managementRouteStrategyDeleteStore) CreatePublicRouteStrategy(
	context.Context,
	port.PublicRouteStrategyCreateInput,
) (port.PublicRouteStrategySummary, error) {
	return port.PublicRouteStrategySummary{}, errors.New("unexpected route strategy create")
}

func (s *managementRouteStrategyDeleteStore) UpdatePublicRouteStrategy(
	context.Context,
	port.PublicRouteStrategyUpdateInput,
) (port.PublicRouteStrategySummary, bool, error) {
	return port.PublicRouteStrategySummary{}, false, errors.New("unexpected route strategy update")
}

func (s *managementRouteStrategyDeleteStore) DeletePublicRouteStrategy(
	ctx context.Context,
	routeStrategyID string,
	systemAccountID string,
) (bool, error) {
	s.deleteCalls++
	s.deleteRouteStrategyID = routeStrategyID
	s.deleteSystemAccountID = systemAccountID
	if !managementRouteStrategyDeleteInTx(ctx) {
		s.allMutationCallsWereInTx = false
	}
	managementRouteStrategyDeleteEvent(s.events, "delete")
	if s.deleteErr != nil {
		return false, s.deleteErr
	}
	return s.deleted, nil
}

func (s *managementRouteStrategyDeleteStore) PublicRouteStrategyAPIKeyCount(
	ctx context.Context,
	_ string,
	_ string,
) (int64, error) {
	s.countCalls++
	if !managementRouteStrategyDeleteInTx(ctx) {
		s.allMutationCallsWereInTx = false
	}
	managementRouteStrategyDeleteEvent(s.events, "count")
	if s.countErr != nil {
		return 0, s.countErr
	}
	return s.count, nil
}

type managementRouteStrategyDeleteForbiddenStore struct {
	port.PublicRouteStrategyStore
	calls []string
}

func (s *managementRouteStrategyDeleteForbiddenStore) unexpected(
	method string,
) error {
	s.calls = append(s.calls, method)
	return errors.New("ordinary createStore must not be used by Delete: " + method)
}

func (s *managementRouteStrategyDeleteForbiddenStore) FindPublicRouteStrategyTargetByUsername(
	context.Context,
	string,
) (port.PublicGroupTarget, bool, error) {
	return port.PublicGroupTarget{}, false, s.unexpected(
		"FindPublicRouteStrategyTargetByUsername",
	)
}

func (s *managementRouteStrategyDeleteForbiddenStore) FindPublicRouteStrategyTargetByID(
	context.Context,
	string,
) (port.PublicGroupTarget, bool, error) {
	return port.PublicGroupTarget{}, false, s.unexpected(
		"FindPublicRouteStrategyTargetByID",
	)
}

func (s *managementRouteStrategyDeleteForbiddenStore) ListPublicRouteStrategies(
	context.Context,
	port.PublicRouteStrategyListInput,
) (port.PublicRouteStrategyListPage, error) {
	return port.PublicRouteStrategyListPage{}, s.unexpected(
		"ListPublicRouteStrategies",
	)
}

func (s *managementRouteStrategyDeleteForbiddenStore) FindPublicRouteStrategyByID(
	context.Context,
	string,
) (port.PublicRouteStrategySummary, bool, error) {
	return port.PublicRouteStrategySummary{}, false, s.unexpected(
		"FindPublicRouteStrategyByID",
	)
}

func (s *managementRouteStrategyDeleteForbiddenStore) FindPublicRouteStrategyBindableGroups(
	context.Context,
	string,
	[]string,
) ([]port.PublicRouteStrategyBindableGroup, error) {
	return nil, s.unexpected("FindPublicRouteStrategyBindableGroups")
}

func (s *managementRouteStrategyDeleteForbiddenStore) CreatePublicRouteStrategy(
	context.Context,
	port.PublicRouteStrategyCreateInput,
) (port.PublicRouteStrategySummary, error) {
	return port.PublicRouteStrategySummary{}, s.unexpected(
		"CreatePublicRouteStrategy",
	)
}

func (s *managementRouteStrategyDeleteForbiddenStore) UpdatePublicRouteStrategy(
	context.Context,
	port.PublicRouteStrategyUpdateInput,
) (port.PublicRouteStrategySummary, bool, error) {
	return port.PublicRouteStrategySummary{}, false, s.unexpected(
		"UpdatePublicRouteStrategy",
	)
}

func (s *managementRouteStrategyDeleteForbiddenStore) DeletePublicRouteStrategy(
	context.Context,
	string,
	string,
) (bool, error) {
	return false, s.unexpected("DeletePublicRouteStrategy")
}

func (s *managementRouteStrategyDeleteForbiddenStore) PublicRouteStrategyAPIKeyCount(
	context.Context,
	string,
	string,
) (int64, error) {
	return 0, s.unexpected("PublicRouteStrategyAPIKeyCount")
}

type managementRouteStrategyDeleteTxContextKey struct{}

type managementRouteStrategyDeleteTransactor struct {
	store       port.PublicRouteStrategyStore
	events      *[]string
	afterErr    error
	afterCommit func()
	calls       int
	commits     int
	rollbacks   int
}

func (t *managementRouteStrategyDeleteTransactor) PublicRouteStrategyInTx(
	ctx context.Context,
	fn func(context.Context, port.PublicRouteStrategyStore) error,
) error {
	t.calls++
	managementRouteStrategyDeleteEvent(t.events, "tx:begin")
	txCtx := context.WithValue(ctx, managementRouteStrategyDeleteTxContextKey{}, true)
	if err := fn(txCtx, t.store); err != nil {
		t.rollbacks++
		managementRouteStrategyDeleteEvent(t.events, "tx:rollback")
		return err
	}
	if t.afterErr != nil {
		t.rollbacks++
		managementRouteStrategyDeleteEvent(t.events, "tx:commit-failed")
		return t.afterErr
	}
	t.commits++
	managementRouteStrategyDeleteEvent(t.events, "tx:commit")
	if t.afterCommit != nil {
		t.afterCommit()
	}
	return nil
}

func managementRouteStrategyDeleteInTx(ctx context.Context) bool {
	inTx, _ := ctx.Value(managementRouteStrategyDeleteTxContextKey{}).(bool)
	return inTx
}

type managementRouteStrategyDeleteInvalidator struct {
	events *[]string
	err    error

	calls       int
	reason      string
	contextErr  error
	hasDeadline bool
	deadline    time.Time
}

func (i *managementRouteStrategyDeleteInvalidator) InvalidateGatewayRuntime(
	ctx context.Context,
	reason string,
) error {
	i.calls++
	i.reason = reason
	i.contextErr = ctx.Err()
	i.deadline, i.hasDeadline = ctx.Deadline()
	managementRouteStrategyDeleteEvent(i.events, "invalidate")
	return i.err
}

func managementRouteStrategyDeleteEvent(events *[]string, event string) {
	if events != nil {
		*events = append(*events, event)
	}
}

func astReceiverTypeName(expression ast.Expr) string {
	switch receiver := expression.(type) {
	case *ast.Ident:
		return receiver.Name
	case *ast.StarExpr:
		return astReceiverTypeName(receiver.X)
	default:
		return ""
	}
}

func namedSQLQuerySection(source string, queryName string) (string, error) {
	lines := strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n")
	matches := make([]string, 0, 1)
	for index := 0; index < len(lines); {
		name, isHeader := sqlNamedQueryHeader(lines[index])
		if !isHeader {
			index++
			continue
		}
		sectionStart := index + 1
		sectionEnd := sectionStart
		for sectionEnd < len(lines) {
			if _, nextIsHeader := sqlNamedQueryHeader(lines[sectionEnd]); nextIsHeader {
				break
			}
			sectionEnd++
		}
		if name == queryName {
			matches = append(
				matches,
				strings.Join(lines[sectionStart:sectionEnd], "\n"),
			)
		}
		index = sectionEnd
	}
	if len(matches) != 1 {
		return "", errors.New(
			"expected exactly one named SQL query section for " +
				queryName +
				", found " +
				fmt.Sprint(len(matches)),
		)
	}
	return matches[0], nil
}

func sqlNamedQueryHeader(line string) (string, bool) {
	const prefix = "-- name:"
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, prefix) {
		return "", false
	}
	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(trimmed, prefix)))
	if len(fields) < 2 || !strings.HasPrefix(fields[1], ":") {
		return "", false
	}
	return fields[0], true
}

func sqlSectionHasIndependentForUpdate(section string) bool {
	for _, line := range strings.Split(strings.ReplaceAll(section, "\r\n", "\n"), "\n") {
		normalized := strings.TrimSpace(line)
		normalized = strings.TrimSpace(strings.TrimSuffix(normalized, ";"))
		fields := strings.Fields(normalized)
		if len(fields) == 2 &&
			strings.EqualFold(fields[0], "FOR") &&
			strings.EqualFold(fields[1], "UPDATE") {
			return true
		}
	}
	return false
}
