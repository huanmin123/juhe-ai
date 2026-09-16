package apikeys

// w11a coverage arms (part 2): schedule document matrix (date ranges,
// exceptions, occurrence math, timezone conversions) and the guarded-route
// auth/scope/decode branches over the HTTP surface.

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func w11aSchedule(t *testing.T, document string) *AvailabilitySchedule {
	t.Helper()
	schedule, err := NormalizeSchedule(w11aScheduleObject(document))
	if err != nil {
		t.Fatal(err)
	}
	return schedule
}

func w11aScheduleObject(document string) map[string]any {
	return w9eScheduleObject(document)
}

func TestW11AScheduleDocumentMatrix(t *testing.T) {
	// Unknown top-level key.
	if _, err := NormalizeSchedule(map[string]any{"enabled": true, "mode": "allow_windows", "bogus": 1}); err == nil {
		t.Fatal("unknown schedule key must fail")
	}
	// Stored JSON corruption.
	if _, err := parseScheduleJSONWithDefault("{bad", func() string { return "UTC" }); err == nil {
		t.Fatal("corrupt stored schedule must fail")
	}
	// Date range arms.
	for document, wantErr := range map[string]bool{
		`{"enabled":true,"mode":"allow_windows","dateRange":null}`:                                       true,
		`{"enabled":true,"mode":"allow_windows","dateRange":"x"}`:                                        true,
		`{"enabled":true,"mode":"allow_windows","dateRange":{"bogus":1}}`:                                true,
		`{"enabled":true,"mode":"allow_windows","dateRange":{"startDate":"2026-13-01"}}`:                 true,
		`{"enabled":true,"mode":"allow_windows","dateRange":{"startDate":"not-a-date"}}`:                 true,
		`{"enabled":true,"mode":"allow_windows","dateRange":{"startDate":5}}`:                            true,
		`{"enabled":true,"mode":"allow_windows","dateRange":{"startDate":"2026-03-01","endDate":"2026-02-01"}}`: true,
		`{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"18:00"}],"dateRange":{}}`: false,
	} {
		_, err := NormalizeSchedule(w11aScheduleObject(document))
		if wantErr && err == nil {
			t.Fatalf("dateRange %s must fail", document)
		}
		if !wantErr && err != nil {
			t.Fatalf("dateRange %s = %v", document, err)
		}
	}
	// Exception arms.
	for _, document := range []string{
		`{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"18:00"}],"exceptions":null}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"18:00"}],"exceptions":"x"}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"18:00"}],"exceptions":[{}]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"18:00"}],"exceptions":[{"date":"2026-01-01"}]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"18:00"}],"exceptions":[{"bogus":1}]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"18:00"}],"exceptions":[{"date":"","action":"deny"}]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"18:00"}],"exceptions":[{"date":"2026-01-01","action":"nope"}]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"18:00"}],"exceptions":[{"date":"2026-01-01","action":"deny","windows":null}]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"18:00"}],"exceptions":[{"date":"2026-01-01","action":"deny","windows":[]}]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"18:00"}],"exceptions":[{"date":"2026-01-01","action":"allow"}]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"18:00"}],"exceptions":[{"date":"2026-01-01","action":"allow","windows":null}]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"18:00"}],"exceptions":[{"date":"2026-01-01","action":"allow","windows":[{"start":"9:00","end":"18:00"}]}]}`,
	} {
		if _, err := NormalizeSchedule(w11aScheduleObject(document)); err == nil {
			t.Fatalf("exception %s must fail", document)
		}
	}
	// An empty exceptions array normalizes to no exceptions.
	emptyExceptions := w11aSchedule(t, `{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"18:00"}],"exceptions":[]}`)
	if emptyExceptions.Exceptions != nil {
		t.Fatalf("empty exceptions = %+v", emptyExceptions.Exceptions)
	}

	// Non-numeric day entries inside a top-level window.
	if _, err := NormalizeSchedule(w11aScheduleObject(`{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":["x"],"start":"09:00","end":"18:00"}]}`)); err == nil {
		t.Fatal("non-numeric daysOfWeek must fail")
	}

	// A valid deny exception normalizes without windows.
	deny := w11aSchedule(t, `{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"18:00"}],"exceptions":[{"date":"2026-02-02","action":"deny"}]}`)
	if len(deny.Exceptions) != 1 || deny.Exceptions[0].Action != "deny" || len(deny.Exceptions[0].Windows) != 0 {
		t.Fatalf("deny exception = %+v", deny.Exceptions)
	}
	// A valid allow exception carries start/end-only windows.
	allow := w11aSchedule(t, `{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"18:00"}],"exceptions":[{"date":"2026-02-03","action":"allow","windows":[{"start":"10:00","end":"12:00"}]}]}`)
	if len(allow.Exceptions) != 1 || len(allow.Exceptions[0].Windows) != 1 {
		t.Fatalf("allow exception = %+v", allow.Exceptions)
	}

	// NextScheduleCheckAt over a bounded range with exceptions.
	now := time.Date(2026, 2, 1, 8, 0, 0, 0, time.UTC)
	bounded := w11aSchedule(t, `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"18:00"}],"dateRange":{"startDate":"2026-02-02","endDate":"2026-02-09"},"exceptions":[{"date":"2026-02-02","action":"deny"},{"date":"2026-02-03","action":"allow","windows":[{"start":"10:00","end":"12:00"}]}]}`)
	next, ok := NextScheduleCheckAt(bounded, now)
	if !ok || !strings.HasPrefix(next, "2026-") {
		t.Fatalf("next check = %q, %v", next, ok)
	}
	// Outside the range entirely: the fallback horizon answers.
	late := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if _, ok := NextScheduleCheckAt(bounded, late); !ok {
		t.Fatal("fallback horizon must still answer")
	}

	// Overnight windows land the end boundary on the next date.
	overnight := w11aSchedule(t, `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"22:00","end":"02:00"}]}`)
	next, ok = NextScheduleCheckAt(overnight, now)
	if !ok {
		t.Fatal("overnight schedule must produce a boundary")
	}
	// A schedule whose days never match still answers via the fallback.
	rare := w11aSchedule(t, `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[7],"start":"01:00","end":"02:00"}]}`)
	if _, ok := NextScheduleCheckAt(rare, now); !ok {
		t.Fatal("non-matching schedule must answer with the horizon")
	}

	// Timezone conversion guards.
	if value, ok := zonedLocalMinuteToUTC("2026-01-01", 0, "W11A/Nowhere"); ok || value != 0 {
		t.Fatalf("unknown timezone = %d, %v", value, ok)
	}
	if _, ok := NextScheduleCheckAt(&AvailabilitySchedule{Enabled: false}, now); ok {
		t.Fatal("disabled schedule must not answer")
	}
}

// ---------------------------------------------------------------------------
// Guarded route branches over HTTP: unauthenticated, scope gate, decode.
// ---------------------------------------------------------------------------

func TestW11AGuardedRouteBranchArms(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		env := newTestEnv(t)
		if status, _ := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"w11a"}`); status != http.StatusUnauthorized {
			t.Fatalf("create unauthenticated = %d", status)
		}
		if status, _ := env.do(t, http.MethodPost, "/__aisys__/api/api-keys/x/refresh-key", ""); status != http.StatusUnauthorized {
			t.Fatalf("refresh unauthenticated = %d", status)
		}
		if status, _ := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/x", `{"expectedRevision":"2026-01-01T00:00:00.000Z"}`); status != http.StatusUnauthorized {
			t.Fatalf("patch unauthenticated = %d", status)
		}
		if status, _ := env.do(t, http.MethodDelete, "/__aisys__/api/api-keys/x", ""); status != http.StatusUnauthorized {
			t.Fatalf("delete unauthenticated = %d", status)
		}
		if status, _ := env.do(t, http.MethodGet, "/__aisys__/api/api-keys/x", ""); status != http.StatusUnauthorized {
			t.Fatalf("find unauthenticated = %d", status)
		}
	})

	t.Run("scope_gate_and_decode", func(t *testing.T) {
		env := newTestEnv(t)
		env.login(t, "root", "root-pass", "super_admin")
		if status, body := env.do(t, http.MethodPost, "/__aisys__/api/api-keys?systemAccountId=%20", `{"name":"w11a"}`); status != http.StatusBadRequest {
			t.Fatalf("create scope gate = %d (%v)", status, body)
		}
		if status, _ := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", "{bad json"); status != http.StatusBadRequest {
			t.Fatalf("create bad json = %d", status)
		}
		if status, _ := env.do(t, http.MethodPost, "/__aisys__/api/api-keys/x/refresh-key?systemAccountId=+", ""); status != http.StatusBadRequest {
			t.Fatalf("refresh scope gate = %d", status)
		}
		if status, _ := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/x?systemAccountId=+", "{bad json"); status != http.StatusBadRequest {
			t.Fatalf("patch scope/decode = %d", status)
		}
		if status, _ := env.do(t, http.MethodDelete, "/__aisys__/api/api-keys/x?systemAccountId=+", ""); status != http.StatusBadRequest {
			t.Fatalf("delete scope gate = %d", status)
		}
	})

	t.Run("refresh_and_delete_missing", func(t *testing.T) {
		env := newTestEnv(t)
		env.login(t, "root", "root-pass", "super_admin")
		if status, _ := env.do(t, http.MethodPost, "/__aisys__/api/api-keys/w11a-missing/refresh-key", ""); status != http.StatusNotFound {
			t.Fatalf("refresh missing = %d", status)
		}
		if status, _ := env.do(t, http.MethodDelete, "/__aisys__/api/api-keys/w11a-missing", ""); status != http.StatusNotFound {
			t.Fatalf("delete missing = %d", status)
		}
	})

	t.Run("delete_blank_name_falls_back_to_id", func(t *testing.T) {
		env := newTestEnv(t)
		ownerID := env.login(t, "root", "root-pass", "super_admin")
		env.seedDefaultRouteStrategy(t, ownerID, "rs-w11a")
		status, body := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"w11a-key"}`)
		if status != http.StatusCreated {
			t.Fatalf("create = %d (%v)", status, body)
		}
		created := dataMap(t, body)["id"].(string)
		env.exec(t, `UPDATE api_keys SET name = '' WHERE id = ?`, created)
		if status, _ := env.do(t, http.MethodDelete, "/__aisys__/api/api-keys/"+created, ""); status != http.StatusNoContent {
			t.Fatalf("delete blank name = %d", status)
		}
	})

	t.Run("patch_long_description_400", func(t *testing.T) {
		env := newTestEnv(t)
		ownerID := env.login(t, "root", "root-pass", "super_admin")
		env.seedDefaultRouteStrategy(t, ownerID, "rs-w11a")
		status, body := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"w11a-key"}`)
		if status != http.StatusCreated {
			t.Fatalf("create = %d (%v)", status, body)
		}
		created := dataMap(t, body)["id"].(string)
		revision := env.queryCell(t, `SELECT updated_at FROM api_keys WHERE id = ?`, created)
		long := strings.Repeat("x", 201)
		status, _ = env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+created,
			`{"expectedRevision":"`+revision+`","description":"`+long+`"}`)
		if status != http.StatusBadRequest {
			t.Fatalf("long description = %d", status)
		}
		// Unknown route strategy guard.
		status, _ = env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+created,
			`{"expectedRevision":"`+revision+`","routeStrategyId":"w11a-missing"}`)
		if status != http.StatusBadRequest {
			t.Fatalf("unknown strategy = %d", status)
		}
	})
}
