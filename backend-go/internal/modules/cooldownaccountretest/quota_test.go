package cooldownaccountretest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestQuotaEligibilityBatchesOwnerDirectAndTeamDecisions(t *testing.T) {
	now := time.Date(2026, 7, 5, 17, 0, 0, 0, time.UTC)
	subjects := &cooldownAccountRetestQuotaSubjectsStub{subjects: []port.CooldownAccountRetestQuotaSubject{
		{AccountID: "owner", AccessType: port.CooldownAccountRetestQuotaAccessOwner, AuthorizationValid: true},
		{
			AccountID: "direct", AccessType: port.CooldownAccountRetestQuotaAccessAuthorized,
			AuthorizationID: "auth_direct", SystemAccountID: "sys_direct", AuthorizationValid: true,
			DirectLimits: port.ManagementRequestQuotaLimits{Daily: &port.ManagementRequestQuotaLimit{Enabled: true, Limit: 10}},
		},
		{
			AccountID: "team", AccessType: port.CooldownAccountRetestQuotaAccessAuthorized,
			AuthorizationID: "auth_team", SystemAccountID: "sys_team", EffectiveSourceTeamID: "team_ops", AuthorizationValid: true,
			DirectLimits: port.ManagementRequestQuotaLimits{Total: &port.ManagementRequestQuotaLimit{Enabled: true, Limit: 100}},
			TeamLimits:   port.ManagementRequestQuotaLimits{Hourly: &port.ManagementRequestHourlyQuotaLimit{Enabled: true, Hours: 6, Limit: 5}},
		},
		{
			AccountID: "unlimited", AccessType: port.CooldownAccountRetestQuotaAccessAuthorized,
			AuthorizationID: "auth_unlimited", SystemAccountID: "sys_unlimited", AuthorizationValid: true,
		},
		{
			AccountID: "invalid", AccessType: port.CooldownAccountRetestQuotaAccessAuthorized,
			AuthorizationID: "auth_invalid", SystemAccountID: "sys_invalid", AuthorizationValid: false,
		},
	}}
	costs := &cooldownAccountRetestQuotaCostsStub{costs: map[string]port.GatewayQuotaCosts{
		"sys_direct\x00account_authorization\x00auth_direct\x002026-07-06\x002026-07-06\x002026-07\x00": {Daily: 9},
		"sys_team\x00account_authorization\x00auth_team\x002026-07-06\x002026-07-06\x002026-07\x00":     {Total: 1},
		"sys_team\x00account_authorization_team\x00team:team_ops\x002026-07-06\x002026-07-06\x002026-07\x006": {
			Hourly: 5,
		},
	}}
	timezones := &cooldownAccountRetestQuotaTimezoneStub{timezone: "Asia/Shanghai", found: true}
	filter := QuotaEligibility{Subjects: subjects, Costs: costs, Timezones: timezones}
	candidates := []port.CooldownAccountRetestCandidate{
		{ID: "owner"}, {ID: "direct"}, {ID: "team"}, {ID: "unlimited"}, {ID: "invalid"}, {ID: "missing"}, {ID: "direct"},
	}

	got, err := filter.EligibleByAccountID(context.Background(), candidates, now)
	if err != nil {
		t.Fatalf("EligibleByAccountID() error = %v", err)
	}
	want := map[string]bool{
		"owner": true, "direct": true, "team": false, "unlimited": true, "invalid": false, "missing": false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("EligibleByAccountID() = %#v, want %#v", got, want)
	}
	if subjects.calls != 1 || !reflect.DeepEqual(subjects.accountIDs, []string{"owner", "direct", "team", "unlimited", "invalid", "missing"}) {
		t.Fatalf("subject calls/ids = %d/%#v", subjects.calls, subjects.accountIDs)
	}
	if costs.calls != 1 || len(costs.inputs) != 3 {
		t.Fatalf("cost calls/inputs = %d/%#v, want one batch with three checks", costs.calls, costs.inputs)
	}
	if timezones.calls != 1 {
		t.Fatalf("timezone calls = %d, want one", timezones.calls)
	}
}

func TestQuotaEligibilityFailsClosedWhenCostEntryIsMissing(t *testing.T) {
	subjects := &cooldownAccountRetestQuotaSubjectsStub{subjects: []port.CooldownAccountRetestQuotaSubject{{
		AccountID: "authorized", AccessType: port.CooldownAccountRetestQuotaAccessAuthorized,
		AuthorizationID: "auth", SystemAccountID: "sys", AuthorizationValid: true,
		DirectLimits: port.ManagementRequestQuotaLimits{Total: &port.ManagementRequestQuotaLimit{Enabled: true, Limit: 10}},
	}}}
	filter := QuotaEligibility{
		Subjects: subjects,
		Costs:    &cooldownAccountRetestQuotaCostsStub{costs: map[string]port.GatewayQuotaCosts{}},
		Timezones: &cooldownAccountRetestQuotaTimezoneStub{
			timezone: "UTC", found: true,
		},
	}

	got, err := filter.EligibleByAccountID(context.Background(), []port.CooldownAccountRetestCandidate{{ID: "authorized"}}, time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("EligibleByAccountID() error = %v", err)
	}
	if got["authorized"] {
		t.Fatalf("eligible = %#v, want missing aggregate cost key to fail closed", got)
	}
}

func TestQuotaEligibilityFailsClosedForInvalidLimitsWithoutReadingCosts(t *testing.T) {
	costs := &cooldownAccountRetestQuotaCostsStub{costs: map[string]port.GatewayQuotaCosts{}}
	filter := QuotaEligibility{
		Subjects: &cooldownAccountRetestQuotaSubjectsStub{subjects: []port.CooldownAccountRetestQuotaSubject{{
			AccountID: "authorized", AccessType: port.CooldownAccountRetestQuotaAccessAuthorized,
			AuthorizationID: "auth", SystemAccountID: "sys", AuthorizationValid: true,
			DirectLimits: port.ManagementRequestQuotaLimits{Hourly: &port.ManagementRequestHourlyQuotaLimit{Enabled: true, Hours: 0, Limit: 10}},
		}}},
		Costs:     costs,
		Timezones: &cooldownAccountRetestQuotaTimezoneStub{timezone: "UTC", found: true},
	}

	got, err := filter.EligibleByAccountID(context.Background(), []port.CooldownAccountRetestCandidate{{ID: "authorized"}}, time.Now())
	if err != nil {
		t.Fatalf("EligibleByAccountID() error = %v", err)
	}
	if got["authorized"] || costs.calls != 0 || len(costs.inputs) != 0 {
		t.Fatalf("eligible/cost calls/inputs = %#v/%d/%#v, want fail closed without an empty cost read", got, costs.calls, costs.inputs)
	}
}

func TestQuotaEligibilityClearsProvisionalOwnerOnCostFailure(t *testing.T) {
	costs := &cooldownAccountRetestQuotaCostsStub{err: errors.New("stats unavailable")}
	filter := QuotaEligibility{
		Subjects: &cooldownAccountRetestQuotaSubjectsStub{subjects: []port.CooldownAccountRetestQuotaSubject{
			{AccountID: "owner", AccessType: port.CooldownAccountRetestQuotaAccessOwner, AuthorizationValid: true},
			{
				AccountID: "authorized", AccessType: port.CooldownAccountRetestQuotaAccessAuthorized,
				AuthorizationID: "auth", SystemAccountID: "sys", AuthorizationValid: true,
				DirectLimits: port.ManagementRequestQuotaLimits{Total: &port.ManagementRequestQuotaLimit{Enabled: true, Limit: 10}},
			},
		}},
		Costs:     costs,
		Timezones: &cooldownAccountRetestQuotaTimezoneStub{timezone: "UTC", found: true},
	}

	got, err := filter.EligibleByAccountID(context.Background(), []port.CooldownAccountRetestCandidate{{ID: "owner"}, {ID: "authorized"}}, time.Now())
	if err == nil {
		t.Fatal("EligibleByAccountID() error = nil, want aggregate read error")
	}
	if got["owner"] || got["authorized"] {
		t.Fatalf("eligible = %#v, want every candidate false on cost failure", got)
	}
}

func TestQuotaEligibilityReturnsFalseMapOnDependencyFailure(t *testing.T) {
	filter := QuotaEligibility{}
	got, err := filter.EligibleByAccountID(context.Background(), []port.CooldownAccountRetestCandidate{{ID: "a"}}, time.Now())
	if err == nil {
		t.Fatal("EligibleByAccountID() error = nil, want missing dependency error")
	}
	if got["a"] {
		t.Fatalf("eligible = %#v, want false on dependency error", got)
	}
}

func TestQuotaEligibilityRejectsInvalidTimezoneBeforeSubjectRead(t *testing.T) {
	subjects := &cooldownAccountRetestQuotaSubjectsStub{}
	filter := QuotaEligibility{
		Subjects:  subjects,
		Costs:     &cooldownAccountRetestQuotaCostsStub{},
		Timezones: &cooldownAccountRetestQuotaTimezoneStub{timezone: "Mars/Olympus", found: true},
	}
	got, err := filter.EligibleByAccountID(context.Background(), []port.CooldownAccountRetestCandidate{{ID: "a"}}, time.Now())
	if err == nil {
		t.Fatal("EligibleByAccountID() error = nil, want invalid timezone error")
	}
	if got["a"] || subjects.calls != 0 {
		t.Fatalf("eligible/subject calls = %#v/%d, want fail closed before DB subject read", got, subjects.calls)
	}
}

func TestQuotaEligibilityRejectsMissingTimezoneBeforeSubjectRead(t *testing.T) {
	for name, timezone := range map[string]*cooldownAccountRetestQuotaTimezoneStub{
		"missing": {found: false},
		"blank":   {timezone: "  ", found: true},
	} {
		t.Run(name, func(t *testing.T) {
			subjects := &cooldownAccountRetestQuotaSubjectsStub{}
			filter := QuotaEligibility{
				Subjects: subjects, Costs: &cooldownAccountRetestQuotaCostsStub{}, Timezones: timezone,
			}
			got, err := filter.EligibleByAccountID(context.Background(), []port.CooldownAccountRetestCandidate{{ID: "a"}}, time.Now())
			if err == nil || got["a"] || subjects.calls != 0 {
				t.Fatalf("eligible/error/subject calls = %#v/%v/%d, want fail closed", got, err, subjects.calls)
			}
		})
	}
}

type cooldownAccountRetestQuotaSubjectsStub struct {
	subjects   []port.CooldownAccountRetestQuotaSubject
	err        error
	calls      int
	accountIDs []string
}

func (s *cooldownAccountRetestQuotaSubjectsStub) LoadCooldownAccountRetestQuotaSubjects(_ context.Context, accountIDs []string, _ time.Time) ([]port.CooldownAccountRetestQuotaSubject, error) {
	s.calls++
	s.accountIDs = append([]string(nil), accountIDs...)
	if s.err != nil {
		return nil, s.err
	}
	return append([]port.CooldownAccountRetestQuotaSubject(nil), s.subjects...), nil
}

type cooldownAccountRetestQuotaCostsStub struct {
	costs  map[string]port.GatewayQuotaCosts
	err    error
	calls  int
	inputs []port.GatewayQuotaCostLookupInput
}

func (s *cooldownAccountRetestQuotaCostsStub) LoadGatewayQuotaSnapshotCosts(_ context.Context, inputs []port.GatewayQuotaCostLookupInput) (map[string]port.GatewayQuotaCosts, error) {
	s.calls++
	s.inputs = append([]port.GatewayQuotaCostLookupInput(nil), inputs...)
	if s.err != nil {
		return nil, s.err
	}
	output := make(map[string]port.GatewayQuotaCosts, len(s.costs))
	for key, costs := range s.costs {
		output[key] = costs
	}
	return output, nil
}

type cooldownAccountRetestQuotaTimezoneStub struct {
	timezone string
	found    bool
	err      error
	calls    int
}

func (s *cooldownAccountRetestQuotaTimezoneStub) GetManagementUsageStatsTimezone(context.Context) (string, bool, error) {
	s.calls++
	return s.timezone, s.found, s.err
}

var (
	_ port.CooldownAccountRetestQuotaSubjectReader = (*cooldownAccountRetestQuotaSubjectsStub)(nil)
	_ port.GatewayQuotaCostReader                  = (*cooldownAccountRetestQuotaCostsStub)(nil)
	_ port.ManagementUsageStatsTimezoneReader      = (*cooldownAccountRetestQuotaTimezoneStub)(nil)
)
