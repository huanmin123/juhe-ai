package oauthrefresh

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

const availabilityScheduleJSON = `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"09:00","end":"18:00"}]}`

func seedApiKeyWithSchedule(t *testing.T, db *sql.DB, id, scheduleJSON, nextCheckAt, status string) {
	t.Helper()
	var scheduleArg, nextArg any
	if scheduleJSON != "" {
		scheduleArg = scheduleJSON
	}
	if nextCheckAt != "" {
		nextArg = nextCheckAt
	}
	if status == "" {
		status = "active"
	}
	_, err := db.Exec(`INSERT INTO api_keys (id, status, availability_schedule_json, availability_schedule_next_check_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`, id, status, scheduleArg, nextArg, isoMillis(defaultNow()))
	if err != nil {
		t.Fatal(err)
	}
}

func seedAccountWithSchedule(t *testing.T, db *sql.DB, id, scheduleJSON, nextCheckAt, status string, deleted bool) {
	t.Helper()
	var scheduleArg, nextArg any
	if scheduleJSON != "" {
		scheduleArg = scheduleJSON
	}
	if nextCheckAt != "" {
		nextArg = nextCheckAt
	}
	if status == "" {
		status = "active"
	}
	deletedArg := any(nil)
	if deleted {
		deletedArg = isoMillis(defaultNow())
	}
	_, err := db.Exec(`INSERT INTO accounts (id, provider_code, provider_protocol_profile_id, name, type, status,
		credentials_encrypted, availability_schedule_json, availability_schedule_next_check_at, deleted_at, updated_at)
		VALUES (?, 'gpt', 'profile_gpt_openai_v1', ?, 'api_key', ?, '{}', ?, ?, ?, ?)`,
		id, "账户-"+id, status, scheduleArg, nextArg, deletedArg, isoMillis(defaultNow()))
	if err != nil {
		t.Fatal(err)
	}
}

func TestSyncApiKeyScheduleActivatesAtWindowStart(t *testing.T) {
	store, db, clock := newSweepStore(t)
	// 09:00 UTC Monday: the window start boundary.
	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	_ = clock
	seedApiKeyWithSchedule(t, db, "key-1", availabilityScheduleJSON, "", "disabled")

	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), atStart, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 || result.Activated != 1 || len(result.ChangedIDs) != 1 || result.ChangedIDs[0] != "key-1" {
		t.Fatalf("result=%+v", result)
	}
	var status, nextCheck string
	if err := db.QueryRow(`SELECT status, availability_schedule_next_check_at FROM api_keys WHERE id = 'key-1'`).Scan(&status, &nextCheck); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Fatalf("status=%q", status)
	}
	// The derived next check is the window end the same day.
	if nextCheck != "2026-09-07T18:00:00.000Z" {
		t.Fatalf("next check=%q", nextCheck)
	}
	// The dedupe event row exists.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM api_key_schedule_status_events WHERE event_key LIKE 'key-1:%' AND status = 'active'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("event rows=%d", count)
	}

	// Replaying the same minute skips the event and keeps the status. The
	// row becomes due again by resetting its next check (as a fresh import
	// would).
	if _, err := db.Exec(`UPDATE api_keys SET availability_schedule_next_check_at = NULL WHERE id = 'key-1'`); err != nil {
		t.Fatal(err)
	}
	replay, err := store.SyncApiKeyScheduleStatuses(context.Background(), atStart, 0)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Skipped != 1 || replay.Activated != 0 {
		t.Fatalf("replay=%+v", replay)
	}
	status2, _, _ := readApiKey(t, db, "key-1")
	if status2 != "active" {
		t.Fatalf("status after replay=%q", status2)
	}
}

func readApiKey(t *testing.T, db *sql.DB, id string) (status, nextCheckAt string, updatedAt string) {
	t.Helper()
	if err := db.QueryRow(`SELECT status, COALESCE(availability_schedule_next_check_at,''), updated_at FROM api_keys WHERE id = ?`, id).Scan(&status, &nextCheckAt, &updatedAt); err != nil {
		t.Fatal(err)
	}
	return
}

func TestSyncApiKeyScheduleDisablesAtWindowEnd(t *testing.T) {
	store, db, _ := newSweepStore(t)
	atEnd := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	seedApiKeyWithSchedule(t, db, "key-end", availabilityScheduleJSON, "", "active")
	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), atEnd, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disabled != 1 {
		t.Fatalf("result=%+v", result)
	}
	status, nextCheck, _ := readApiKey(t, db, "key-end")
	if status != "disabled" {
		t.Fatalf("status=%q", status)
	}
	// Next boundary: tomorrow 09:00.
	if nextCheck != "2026-09-08T09:00:00.000Z" {
		t.Fatalf("next check=%q", nextCheck)
	}
}

func TestSyncApiKeyScheduleUnchangedAdvancesNextCheck(t *testing.T) {
	store, db, _ := newSweepStore(t)
	// Off-boundary minute inside the window: no event, next check advances.
	inside := time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC)
	seedApiKeyWithSchedule(t, db, "key-inside", availabilityScheduleJSON, "", "active")
	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), inside, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Unchanged != 1 || result.Activated != 0 || result.Disabled != 0 {
		t.Fatalf("result=%+v", result)
	}
	_, nextCheck, _ := readApiKey(t, db, "key-inside")
	if nextCheck != "2026-09-07T18:00:00.000Z" {
		t.Fatalf("next check=%q", nextCheck)
	}
}

func TestSyncApiKeyScheduleInvalidDisables(t *testing.T) {
	store, db, _ := newSweepStore(t)
	seedApiKeyWithSchedule(t, db, "key-invalid", `{"enabled":true,"mode":"bogus"}`, "", "active")
	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), defaultNow(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Invalid != 1 || len(result.InvalidIDs) != 1 || result.InvalidIDs[0] != "key-invalid" {
		t.Fatalf("result=%+v", result)
	}
	status, nextCheck, _ := readApiKey(t, db, "key-invalid")
	if status != "disabled" || nextCheck != "" {
		t.Fatalf("status=%q next=%q", status, nextCheck)
	}
	// An already-disabled row with an invalid schedule only drops the check.
	seedApiKeyWithSchedule(t, db, "key-invalid-disabled", `{"enabled":true,"mode":"bogus"}`, "", "disabled")
	result, err = store.SyncApiKeyScheduleStatuses(context.Background(), defaultNow(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Invalid != 2 {
		t.Fatalf("second invalid=%+v", result)
	}
}

func TestSyncApiKeyScheduleDueWindowCoversNullNextCheck(t *testing.T) {
	store, db, _ := newSweepStore(t)
	// NULL next_check_at is always due, even off-boundary.
	inside := time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC)
	seedApiKeyWithSchedule(t, db, "key-null", availabilityScheduleJSON, "", "active")
	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), inside, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 || result.Unchanged != 1 {
		t.Fatalf("result=%+v", result)
	}
	// Future next_check_at beyond the due time is not scanned.
	seedApiKeyWithSchedule(t, db, "key-future", availabilityScheduleJSON, "2026-09-07T18:00:00.000Z", "active")
	result, err = store.SyncApiKeyScheduleStatuses(context.Background(), inside, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 0 {
		t.Fatalf("future scanned=%d", result.Scanned)
	}
}

func TestSyncAccountScheduleSemantics(t *testing.T) {
	store, db, _ := newSweepStore(t)
	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)

	// Plain disabled account activates at the boundary.
	seedAccountWithSchedule(t, db, "acc-plain", availabilityScheduleJSON, "", "disabled", false)

	// Active account with an active disable enforcement stays disabled.
	seedAccountWithSchedule(t, db, "acc-enforced", availabilityScheduleJSON, "", "disabled", false)
	if _, err := db.Exec(`INSERT INTO account_quality_enforcements (account_id, state, action) VALUES ('acc-enforced', 'active', 'disable')`); err != nil {
		t.Fatal(err)
	}

	// Non-mutable status (pending_test) only advances the next check.
	seedAccountWithSchedule(t, db, "acc-pending", availabilityScheduleJSON, "", "pending_test", false)

	// Deleted accounts are invisible.
	seedAccountWithSchedule(t, db, "acc-deleted", availabilityScheduleJSON, "", "disabled", true)

	activations := []string{}
	hook := ActivationHookFunc(func(_ context.Context, accountID, _ string) error {
		activations = append(activations, accountID)
		return nil
	})
	result, err := store.SyncAccountScheduleStatuses(context.Background(), atStart, 0, hook)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 3 {
		t.Fatalf("scanned=%d (deleted row must be excluded)", result.Scanned)
	}
	if result.Activated != 1 || result.Unchanged != 2 {
		t.Fatalf("result=%+v", result)
	}
	if len(activations) != 1 || activations[0] != "acc-plain" {
		t.Fatalf("activations=%v", activations)
	}
	// The enforced account stays disabled (guard) and counts unchanged.
	status, _, _ := readAccountStatus(t, db, "acc-enforced")
	if status != "disabled" {
		t.Fatalf("enforced status=%q", status)
	}
}

func readAccountStatus(t *testing.T, db *sql.DB, id string) (string, string, string) {
	t.Helper()
	var status, nextCheck, deleted string
	if err := db.QueryRow(`SELECT status, COALESCE(availability_schedule_next_check_at,''), COALESCE(deleted_at,'') FROM accounts WHERE id = ?`, id).Scan(&status, &nextCheck, &deleted); err != nil {
		t.Fatal(err)
	}
	return status, nextCheck, deleted
}

func TestSyncAccountScheduleInvalidActiveRowDisables(t *testing.T) {
	store, db, _ := newSweepStore(t)
	seedAccountWithSchedule(t, db, "acc-invalid", `{"enabled":true,"mode":"bogus"}`, "", "active", false)
	result, err := store.SyncAccountScheduleStatuses(context.Background(), defaultNow(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Invalid != 1 || result.Disabled != 1 {
		t.Fatalf("result=%+v", result)
	}
	status, _, _ := readAccountStatus(t, db, "acc-invalid")
	if status != "disabled" {
		t.Fatalf("status=%q", status)
	}
}

func TestSyncBatchLimit(t *testing.T) {
	store, db, _ := newSweepStore(t)
	for i := 0; i < 7; i++ {
		seedApiKeyWithSchedule(t, db, "batch-"+string(rune('a'+i)), availabilityScheduleJSON, "", "active")
	}
	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC), 5)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 5 {
		t.Fatalf("scanned=%d want batch limit 5", result.Scanned)
	}
}

func TestSyncActivationHookErrorAborts(t *testing.T) {
	store, db, _ := newSweepStore(t)
	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	seedApiKeyWithSchedule(t, db, "key-hook", availabilityScheduleJSON, "", "disabled")
	seedAccountWithSchedule(t, db, "acc-hook", availabilityScheduleJSON, "", "disabled", false)
	failing := ActivationHookFunc(func(context.Context, string, string) error {
		return context.DeadlineExceeded
	})
	if _, err := store.SyncAccountScheduleStatuses(context.Background(), atStart, 0, failing); err == nil {
		t.Fatal("hook error must abort the sync transaction")
	}
	// The status flip rolled back with the hook failure.
	status, _, _ := readAccountStatus(t, db, "acc-hook")
	if status != "disabled" {
		t.Fatalf("status=%q after failed hook", status)
	}
}
