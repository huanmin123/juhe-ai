package managementresponseinspectionpolicies

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceListReturnsNodeCompatibleDefaultsAndStableManagementRows(t *testing.T) {
	store := &responsePolicyStoreStub{list: []port.ResponseInspectionPolicy{
		{ID: "rip-b", Name: "B", Priority: 20, UpdatedAt: "2026-07-22T09:00:00.000Z"},
		{ID: "rip-a", Name: "A", Priority: 10, UpdatedAt: "2026-07-22T10:00:00.000Z"},
	}}
	result, err := NewService(Options{Store: store}).List(t.Context())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	defaultIDs := make([]string, 0, len(result.DefaultRules))
	for _, policy := range result.DefaultRules {
		defaultIDs = append(defaultIDs, policy.ID)
		if !policy.DefaultRule || policy.Editable {
			t.Fatalf("default policy flags = %+v", policy)
		}
	}
	wantDefaultIDs := []string{
		"default_openai_error_object",
		"default_openai_response_error",
		"default_openai_failed_status",
		"default_codex_response_incomplete",
		"default_codex_compaction_contract",
		"default_gpt_cyber_policy",
		"default_anthropic_error_object",
		"default_gemini_cli_retryable_error",
		"default_gemini_error_object",
	}
	if !reflect.DeepEqual(defaultIDs, wantDefaultIDs) {
		t.Fatalf("default IDs = %#v, want %#v", defaultIDs, wantDefaultIDs)
	}
	if len(result.Policies) != 2 || store.listLimit != MaxManagementPolicies {
		t.Fatalf("policies=%+v listLimit=%d", result.Policies, store.listLimit)
	}
}

func TestServiceCreateNormalizesDefaultsAndInvalidatesAfterCommit(t *testing.T) {
	events := []string{}
	store := &responsePolicyStoreStub{events: &events, providerSupported: true}
	invalidator := &responsePolicyInvalidatorStub{events: &events}
	service := NewService(Options{
		Store:       store,
		Invalidator: invalidator,
		Now:         func() time.Time { return time.Date(2026, 7, 22, 8, 9, 10, 0, time.UTC) },
		NewID:       func(string) string { return "rip-fixed" },
	})

	created, err := service.Create(t.Context(), Input{
		Name:         "  Retry policy  ",
		ScopeType:    "provider",
		ProtocolCode: "openai",
		ProviderCode: textPointer(" gpt "),
		Match: Match{
			ClientProfiles:     []string{"codex"},
			OutputTextExcludes: []string{" secret excluded "},
			ErrorCodes:         []string{" rate_limit "},
		},
		Action: "retry_next_account",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID != "rip-fixed" || created.Name != "Retry policy" || !created.Enabled || created.Priority != 100 {
		t.Fatalf("created = %+v", created)
	}
	if created.ProviderCode == nil || *created.ProviderCode != "gpt" || created.Match.ErrorCodes[0] != "rate_limit" {
		t.Fatalf("normalized provider/match = %+v", created)
	}
	wantEvents := []string{"begin", "capacity", "provider", "create", "commit", "invalidate:response_inspection_policy_created"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
}

func TestServiceCreateUsesJavaScriptTrimSemantics(t *testing.T) {
	store := &responsePolicyStoreStub{providerSupported: true}
	input := validProviderResponsePolicyInput()
	input.Name = "\uFEFF Policy \u3000"
	input.ProviderCode = textPointer("\uFEFFgpt\u3000")
	input.Match.ErrorCodes = []string{"\uFEFFrate_limit\u3000"}
	notes := "\u2028notes\uFEFF"
	input.Notes = &notes
	created, err := NewService(Options{Store: store}).Create(t.Context(), input)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Name != "Policy" || created.ProviderCode == nil || *created.ProviderCode != "gpt" ||
		created.Match.ErrorCodes[0] != "rate_limit" || created.Notes == nil || *created.Notes != "notes" {
		t.Fatalf("created = %+v", created)
	}
}

func TestServiceCreateRejectsInvalidPayloadBeforeTransaction(t *testing.T) {
	tests := []struct {
		name    string
		input   Input
		message string
	}{
		{name: "empty name", input: validResponsePolicyInput(), message: "规则名称不能为空"},
		{name: "scope", input: validResponsePolicyInput(), message: "响应检查策略作用层级无效"},
		{name: "protocol provider", input: validResponsePolicyInput(), message: "协议层响应检查策略不能绑定供应商"},
		{name: "protocol empty provider", input: validResponsePolicyInput(), message: "协议层响应检查策略不能绑定供应商"},
		{name: "provider missing", input: validResponsePolicyInput(), message: "供应商层响应检查策略必须选择供应商"},
		{name: "protocol", input: validResponsePolicyInput(), message: "响应检查策略协议无效"},
		{name: "priority", input: validResponsePolicyInput(), message: "优先级必须是 1-9999 的整数"},
		{name: "action", input: validResponsePolicyInput(), message: "响应检查策略动作无效"},
		{name: "action whitespace", input: validResponsePolicyInput(), message: "响应检查策略动作无效"},
		{name: "only excludes", input: validResponsePolicyInput(), message: "至少需要填写一个匹配条件"},
		{name: "client profile", input: validResponsePolicyInput(), message: "客户端画像无效"},
		{name: "client profile whitespace", input: validResponsePolicyInput(), message: "客户端画像无效"},
		{name: "match item", input: validResponsePolicyInput(), message: "匹配条件不能超过 200 个字符"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := test.input
			switch test.name {
			case "empty name":
				input.Name = " "
			case "scope":
				input.ScopeType = "account"
			case "protocol provider":
				input.ProviderCode = textPointer("gpt")
			case "protocol empty provider":
				input.ProviderCode = textPointer(" ")
			case "provider missing":
				input.ScopeType = "provider"
			case "protocol":
				input.ProtocolCode = "unknown"
			case "priority":
				input.Priority = intPointer(0)
			case "action":
				input.Action = "retry_forever"
			case "action whitespace":
				input.Action = " observe "
			case "only excludes":
				input.Match = Match{ClientProfiles: []string{"codex"}, OutputTextExcludes: []string{"x"}}
			case "client profile":
				input.Match.ClientProfiles = []string{"browser"}
			case "client profile whitespace":
				input.Match.ClientProfiles = []string{" codex "}
			case "match item":
				input.Match.ErrorCodes = []string{string(make([]byte, 201))}
			}
			store := &responsePolicyStoreStub{}
			_, err := NewService(Options{Store: store}).Create(t.Context(), input)
			if ValidationMessage(err) != test.message {
				t.Fatalf("Create() error = %v, message=%q want %q", err, ValidationMessage(err), test.message)
			}
			if store.txCalls != 0 {
				t.Fatalf("transaction calls = %d", store.txCalls)
			}
		})
	}
}

func TestServiceCreateMapsCapacityProviderAndConflictErrors(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*responsePolicyStoreStub)
		input     Input
		want      string
		conflict  bool
	}{
		{name: "capacity", configure: func(s *responsePolicyStoreStub) { s.capacity = MaxManagementPolicies }, input: validResponsePolicyInput(), want: "响应检查策略最多允许 100 条"},
		{name: "provider", configure: func(s *responsePolicyStoreStub) { s.providerSupported = false }, input: validProviderResponsePolicyInput(), want: "响应检查策略供应商必须使用同协议启用档案"},
		{name: "conflict", configure: func(s *responsePolicyStoreStub) { s.createErr = port.ErrResponseInspectionPolicyConflict }, input: validResponsePolicyInput(), want: "响应检查策略写入冲突，请刷新后重试", conflict: true},
		{name: "transaction conflict", configure: func(s *responsePolicyStoreStub) { s.txErr = port.ErrResponseInspectionPolicyConflict }, input: validResponsePolicyInput(), want: "响应检查策略写入冲突，请刷新后重试", conflict: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &responsePolicyStoreStub{providerSupported: true}
			test.configure(store)
			_, err := NewService(Options{Store: store}).Create(t.Context(), test.input)
			if ErrorMessage(err) != test.want || IsConflict(err) != test.conflict {
				t.Fatalf("error=%v message=%q conflict=%v", err, ErrorMessage(err), IsConflict(err))
			}
		})
	}
}

func TestServiceUpdateAndDeleteReturnNotFoundWithoutInvalidation(t *testing.T) {
	for _, operation := range []string{"update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			store := &responsePolicyStoreStub{providerSupported: true}
			invalidator := &responsePolicyInvalidatorStub{}
			service := NewService(Options{Store: store, Invalidator: invalidator})
			var err error
			if operation == "update" {
				_, err = service.Update(t.Context(), "missing", validResponsePolicyInput())
			} else {
				_, err = service.Delete(t.Context(), "missing")
			}
			if !IsNotFound(err) || invalidator.calls != 0 {
				t.Fatalf("error=%v invalidations=%d", err, invalidator.calls)
			}
		})
	}
}

func TestServicePostCommitInvalidationFailureDoesNotRollBackResult(t *testing.T) {
	store := &responsePolicyStoreStub{providerSupported: true}
	invalidator := &responsePolicyInvalidatorStub{err: errors.New("redis unavailable")}
	created, err := NewService(Options{Store: store, Invalidator: invalidator}).Create(t.Context(), validResponsePolicyInput())
	if err != nil || created.ID == "" || store.created.ID == "" || invalidator.calls != 1 {
		t.Fatalf("created=%+v stored=%+v invalidations=%d error=%v", created, store.created, invalidator.calls, err)
	}
}

func TestServiceTransactionFailureSkipsInvalidation(t *testing.T) {
	store := &responsePolicyStoreStub{txErr: errors.New("commit failed"), providerSupported: true}
	invalidator := &responsePolicyInvalidatorStub{}
	_, err := NewService(Options{Store: store, Invalidator: invalidator}).Create(t.Context(), validResponsePolicyInput())
	if err == nil || invalidator.calls != 0 {
		t.Fatalf("error=%v invalidations=%d", err, invalidator.calls)
	}
}

func validResponsePolicyInput() Input {
	return Input{
		Name:         "Policy",
		ScopeType:    "protocol",
		ProtocolCode: "openai",
		Match:        Match{ErrorCodes: []string{"rate_limit"}},
		Action:       "observe",
	}
}

func validProviderResponsePolicyInput() Input {
	input := validResponsePolicyInput()
	input.ScopeType = "provider"
	input.ProviderCode = textPointer("gpt")
	return input
}

func intPointer(value int) *int { return &value }

type responsePolicyStoreStub struct {
	list              []port.ResponseInspectionPolicy
	listLimit         int
	events            *[]string
	txCalls           int
	txErr             error
	capacity          int
	providerSupported bool
	createErr         error
	created           port.ResponseInspectionPolicy
	current           port.ResponseInspectionPolicy
}

func (s *responsePolicyStoreStub) ListResponseInspectionPolicies(_ context.Context, limit int) ([]port.ResponseInspectionPolicy, error) {
	s.listLimit = limit
	return append([]port.ResponseInspectionPolicy(nil), s.list...), nil
}

func (s *responsePolicyStoreStub) ResponseInspectionPolicyInTx(ctx context.Context, fn func(context.Context, port.ResponseInspectionPolicyTxStore) error) error {
	s.txCalls++
	s.event("begin")
	if err := fn(ctx, s); err != nil {
		return err
	}
	if s.txErr != nil {
		return s.txErr
	}
	s.event("commit")
	return nil
}

func (s *responsePolicyStoreStub) CountResponseInspectionPolicies(_ context.Context, _ int) (int, error) {
	s.event("capacity")
	return s.capacity, nil
}

func (s *responsePolicyStoreStub) ResponseInspectionProviderSupportsProtocol(_ context.Context, _, _ string) (bool, error) {
	s.event("provider")
	return s.providerSupported, nil
}

func (s *responsePolicyStoreStub) FindResponseInspectionPolicyForUpdate(_ context.Context, _ string) (port.ResponseInspectionPolicy, bool, error) {
	return s.current, s.current.ID != "", nil
}

func (s *responsePolicyStoreStub) CreateResponseInspectionPolicy(_ context.Context, input port.ResponseInspectionPolicyWriteInput) (port.ResponseInspectionPolicy, error) {
	s.event("create")
	if s.createErr != nil {
		return port.ResponseInspectionPolicy{}, s.createErr
	}
	s.created = responsePolicyFromWriteInput(input)
	return s.created, nil
}

func (s *responsePolicyStoreStub) UpdateResponseInspectionPolicy(_ context.Context, input port.ResponseInspectionPolicyWriteInput) (port.ResponseInspectionPolicy, bool, error) {
	if s.current.ID == "" {
		return port.ResponseInspectionPolicy{}, false, nil
	}
	return responsePolicyFromWriteInput(input), true, nil
}

func (s *responsePolicyStoreStub) DeleteResponseInspectionPolicy(_ context.Context, _ string) (bool, error) {
	return s.current.ID != "", nil
}

func (s *responsePolicyStoreStub) event(value string) {
	if s.events != nil {
		*s.events = append(*s.events, value)
	}
}

func responsePolicyFromWriteInput(input port.ResponseInspectionPolicyWriteInput) port.ResponseInspectionPolicy {
	return port.ResponseInspectionPolicy{
		ID: input.ID, DefaultRule: false, Editable: true, Name: input.Name, Enabled: input.Enabled,
		Priority: input.Priority, ScopeType: input.ScopeType, ProtocolCode: input.ProtocolCode,
		ProviderCode: input.ProviderCode, Match: input.Match, Action: input.Action, Notes: input.Notes,
		CreatedAt: input.CreatedAt, UpdatedAt: input.UpdatedAt,
	}
}

type responsePolicyInvalidatorStub struct {
	events *[]string
	calls  int
	err    error
}

func (s *responsePolicyInvalidatorStub) InvalidateGatewayRuntime(_ context.Context, reason string) error {
	s.calls++
	if s.events != nil {
		*s.events = append(*s.events, "invalidate:"+reason)
	}
	return s.err
}
