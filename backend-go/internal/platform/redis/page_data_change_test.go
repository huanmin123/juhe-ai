package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPageDataChangePublisherBuildsNodeCompatibleResetEventsInOwnerChunks(t *testing.T) {
	publisher, _ := testPageDataChangePublisher()
	owners := []string{" owner-258 ", "owner-001", "owner-001", "", "  "}
	for index := 2; index <= 257; index++ {
		owners = append(owners, fmt.Sprintf("owner-%03d", index))
	}

	events, err := publisher.NewAccountsStaticResetEvents(owners, false)
	if err != nil {
		t.Fatalf("NewAccountsStaticResetEvents() error = %v", err)
	}
	if got, want := len(events), 2; got != want {
		t.Fatalf("event count = %d, want %d", got, want)
	}
	if got, want := len(events[0].OwnerSystemAccountIDs), pageDataMaxEventOwners; got != want {
		t.Fatalf("first owner chunk length = %d, want %d", got, want)
	}
	if got, want := events[0].OwnerSystemAccountIDs[0], "owner-001"; got != want {
		t.Fatalf("first owner = %q, want %q", got, want)
	}
	if got, want := events[1].OwnerSystemAccountIDs, []string{"owner-257", "owner-258"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second owner chunk = %#v, want %#v", got, want)
	}
	for index, event := range events {
		if event.EventID != fmt.Sprintf("event-%d", index+1) ||
			event.Domain != pageDataAccountsStaticDomain ||
			event.Operation != pageDataRangeResetOperation ||
			len(event.FieldMask) != 0 ||
			!event.MembershipChanged || !event.OrderChanged || !event.FilterChanged || !event.PageChanged ||
			event.OccurredAt != "2026-07-18T01:02:03.456Z" || event.AllScopes {
			t.Fatalf("event[%d] = %#v", index, event)
		}
	}
}

func TestNewAccountStaticResetEventsUsesProvidedTime(t *testing.T) {
	events := NewAccountStaticResetEvents(
		[]string{" owner-b ", "owner-a", "owner-a"},
		false,
		time.Date(2026, 7, 18, 1, 2, 3, 456789000, time.UTC),
	)
	if got, want := len(events), 1; got != want {
		t.Fatalf("event count = %d, want %d", got, want)
	}
	if got, want := events[0].OwnerSystemAccountIDs, []string{"owner-a", "owner-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("owners = %#v, want %#v", got, want)
	}
	if got, want := events[0].OccurredAt, "2026-07-18T01:02:03.456Z"; got != want {
		t.Fatalf("occurredAt = %q, want %q", got, want)
	}
	if _, err := uuid.Parse(events[0].EventID); err != nil {
		t.Fatalf("eventId = %q, want UUID: %v", events[0].EventID, err)
	}
}

func TestPageDataChangePublisherBuildsNodeCompatibleAccountStaticUpsertEvent(t *testing.T) {
	publisher, _ := testPageDataChangePublisher()

	event, err := publisher.NewAccountStaticUpsertEvent(AccountStaticUpsertInput{
		AccountID:             "account-1",
		OwnerSystemAccountIDs: []string{" owner-b ", "owner-a", "owner-a", ""},
		FieldMask:             []string{"tags"},
		FilterChanged:         true,
		PageChanged:           true,
	})
	if err != nil {
		t.Fatalf("NewAccountStaticUpsertEvent() error = %v", err)
	}
	if got, want := event, (PageDataChangeEvent{
		EventID:               "event-1",
		Domain:                pageDataAccountsStaticDomain,
		EntityID:              "account-1",
		Operation:             pageDataUpsertOperation,
		FieldMask:             []string{"tags"},
		OwnerSystemAccountIDs: []string{"owner-a", "owner-b"},
		FilterChanged:         true,
		PageChanged:           true,
		OccurredAt:            "2026-07-18T01:02:03.456Z",
	}); !reflect.DeepEqual(got, want) {
		t.Fatalf("event = %#v, want %#v", got, want)
	}
}

func TestPageDataChangePublisherRejectsInvalidAccountStaticUpsertInput(t *testing.T) {
	publisher, _ := testPageDataChangePublisher()
	tooManyOwners := make([]string, pageDataMaxEventOwners+1)
	for index := range tooManyOwners {
		tooManyOwners[index] = fmt.Sprintf("owner-%03d", index)
	}
	tooManyFields := make([]string, pageDataMaxFieldMask+1)
	for index := range tooManyFields {
		tooManyFields[index] = fmt.Sprintf("field-%02d", index)
	}

	for _, input := range []AccountStaticUpsertInput{
		{AccountID: " ", FieldMask: []string{"tags"}},
		{AccountID: "account-1", FieldMask: []string{""}},
		{AccountID: "account-1", FieldMask: tooManyFields},
		{AccountID: "account-1", FieldMask: []string{"tags"}, OwnerSystemAccountIDs: tooManyOwners},
	} {
		if _, err := publisher.NewAccountStaticUpsertEvent(input); err == nil {
			t.Fatalf("NewAccountStaticUpsertEvent(%#v) error = nil", input)
		}
	}
}

func TestPageDataChangePublisherPublishesExactKeysJSONAndFanout(t *testing.T) {
	publisher, capture := testPageDataChangePublisher()
	var calls []pageDataPublishCall
	publisher.runPublish = func(_ context.Context, keys []string, args ...interface{}) error {
		calls = append(calls, pageDataPublishCall{keys: append([]string(nil), keys...), args: append([]interface{}(nil), args...)})
		return nil
	}
	event := PageDataChangeEvent{
		EventID:               "retry-event",
		Domain:                pageDataAccountsStaticDomain,
		Operation:             pageDataRangeResetOperation,
		FieldMask:             []string{},
		OwnerSystemAccountIDs: []string{"owner-a", "owner-b"},
		MembershipChanged:     true,
		OrderChanged:          true,
		FilterChanged:         true,
		PageChanged:           true,
		OccurredAt:            "2026-07-18T01:02:03.456Z",
	}

	if err := publisher.Publish(t.Context(), event); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if got, want := capture.epochGets, []string{"juhe-ai:prod-west:page-data-change:epoch:v2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("epoch GET keys = %#v, want %#v", got, want)
	}
	if strings.Contains(strings.Join(capture.epochGets, ","), ":state:") {
		t.Fatalf("epoch key contains forbidden state segment: %#v", capture.epochGets)
	}
	if got, want := len(calls), 3; got != want {
		t.Fatalf("publish calls = %d, want %d", got, want)
	}
	wantScopes := []string{"global", "owner:owner-a", "owner:owner-b"}
	wantJSON := `{"eventId":"retry-event","domain":"accounts.static","operation":"range_reset","fieldMask":[],"ownerSystemAccountIds":["owner-a","owner-b"],"membershipChanged":true,"orderChanged":true,"filterChanged":true,"pageChanged":true,"occurredAt":"2026-07-18T01:02:03.456Z"}`
	for index, call := range calls {
		prefix := "juhe-ai:prod-west:page-data-change:"
		wantKeys := []string{
			prefix + "sequence:" + wantScopes[index] + ":accounts.static",
			prefix + "log:" + wantScopes[index] + ":accounts.static",
			prefix + "dedupe:" + wantScopes[index] + ":accounts.static",
		}
		if !reflect.DeepEqual(call.keys, wantKeys) {
			t.Fatalf("call[%d] keys = %#v, want %#v", index, call.keys, wantKeys)
		}
		wantArgs := []interface{}{
			"retry-event",
			wantJSON,
			strconv.Itoa(pageDataMaxEventOwners),
			strconv.Itoa(pageDataEventLogTTLSeconds),
		}
		if !reflect.DeepEqual(call.args, wantArgs) {
			t.Fatalf("call[%d] args = %#v, want %#v", index, call.args, wantArgs)
		}
	}

	// The caller owns the event, so retrying reuses its eventId.
	if err := publisher.Publish(t.Context(), event); err != nil {
		t.Fatalf("retry Publish() error = %v", err)
	}
	if got := calls[3].args[0]; got != "retry-event" {
		t.Fatalf("retry eventId = %#v, want retry-event", got)
	}
}

func TestPageDataChangePublisherAllScopesFanoutIsGlobalAndAll(t *testing.T) {
	publisher, _ := testPageDataChangePublisher()
	var keys [][]string
	publisher.runPublish = func(_ context.Context, callKeys []string, _ ...interface{}) error {
		keys = append(keys, append([]string(nil), callKeys...))
		return nil
	}
	events, err := publisher.NewAccountsStaticResetEvents([]string{"owner-a"}, true)
	if err != nil {
		t.Fatalf("NewAccountsStaticResetEvents() error = %v", err)
	}
	if len(events) != 1 || !events[0].AllScopes {
		t.Fatalf("events = %#v", events)
	}
	if err := publisher.Publish(t.Context(), events[0]); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if len(keys) != 2 || !strings.Contains(keys[0][0], ":sequence:global:") || !strings.Contains(keys[1][0], ":sequence:all:") {
		t.Fatalf("allScopes keys = %#v", keys)
	}
}

func TestPageDataChangePublisherValidatesConstructorAndEvents(t *testing.T) {
	if _, err := NewPageDataChangePublisher(nil, "prod"); err == nil {
		t.Fatal("NewPageDataChangePublisher(nil) error = nil")
	}
	client := &Client{}
	for _, namespace := range []string{"", " ", " prod", "prod ", "bad namespace", "bad/namespace", strings.Repeat("a", 65)} {
		if _, err := NewPageDataChangePublisher(client, namespace); err == nil {
			t.Fatalf("NewPageDataChangePublisher(namespace=%q) error = nil", namespace)
		}
	}
	for _, namespace := range []string{"prod", "state:prod", "prod.state_1-v2"} {
		publisher, err := NewPageDataChangePublisher(client, namespace)
		if err != nil {
			t.Fatalf("NewPageDataChangePublisher(namespace=%q) error = %v", namespace, err)
		}
		if got, want := publisher.keyPrefix, "juhe-ai:"+namespace+":page-data-change"; got != want {
			t.Fatalf("keyPrefix = %q, want %q", got, want)
		}
	}
	publisher, _ := testPageDataChangePublisher()
	valid := PageDataChangeEvent{
		EventID: "event-1", Domain: pageDataAccountsStaticDomain, Operation: pageDataRangeResetOperation,
		FieldMask: []string{}, OwnerSystemAccountIDs: []string{"owner-a"},
		MembershipChanged: true, OrderChanged: true, FilterChanged: true, PageChanged: true,
		OccurredAt: "2026-07-18T01:02:03.456Z",
	}
	for _, domain := range []string{
		"accounts.static",
		"accounts.runtime",
		"accounts.options",
		"usage.records",
		"announcements.public",
		"providers.catalog",
		"groups.static",
		"systemAccounts.options",
		"teams.options",
		"routeStrategies.options",
		"stats.overview",
		"stats.accountUsage",
		"stats.aiPerformance",
	} {
		events, err := publisher.NewRangeResetEvents(domain, []string{"owner-a"}, false)
		if err != nil {
			t.Fatalf("NewRangeResetEvents(%s) error = %v", domain, err)
		}
		if len(events) != 1 || events[0].Domain != domain {
			t.Fatalf("NewRangeResetEvents(%s) events = %#v", domain, events)
		}
	}
	cases := []PageDataChangeEvent{
		func() PageDataChangeEvent { value := valid; value.EventID = " "; return value }(),
		func() PageDataChangeEvent { value := valid; value.Domain = "unknown.domain"; return value }(),
		func() PageDataChangeEvent { value := valid; value.Operation = "upsert"; return value }(),
		func() PageDataChangeEvent { value := valid; value.FieldMask = []string{"name"}; return value }(),
		func() PageDataChangeEvent {
			value := valid
			value.OwnerSystemAccountIDs = []string{" owner-a "}
			return value
		}(),
		func() PageDataChangeEvent { value := valid; value.OccurredAt = "invalid"; return value }(),
		func() PageDataChangeEvent { value := valid; value.MembershipChanged = false; return value }(),
	}
	for index, event := range cases {
		if err := publisher.Publish(t.Context(), event); err == nil {
			t.Fatalf("Publish(invalid case %d) error = nil", index)
		}
	}
}

func TestPageDataChangePublisherPublishRejectsNilReceiver(t *testing.T) {
	var publisher *PageDataChangePublisher
	event := PageDataChangeEvent{
		EventID: "event-1", Domain: pageDataAccountsStaticDomain, Operation: pageDataRangeResetOperation,
		FieldMask: []string{}, OwnerSystemAccountIDs: []string{}, MembershipChanged: true,
		OrderChanged: true, FilterChanged: true, PageChanged: true, OccurredAt: "2026-07-18T01:02:03.456Z",
	}
	if err := publisher.Publish(t.Context(), event); err == nil {
		t.Fatal("nil publisher Publish() error = nil")
	}
}

func TestPageDataChangePublisherEpochUsesPersistentGetAndSetNX(t *testing.T) {
	publisher, capture := testPageDataChangePublisher()
	if _, err := publisher.NewAccountsStaticResetEvents(nil, false); err != nil {
		t.Fatalf("NewAccountsStaticResetEvents() error = %v", err)
	}
	event := PageDataChangeEvent{
		EventID: "event-1", Domain: pageDataAccountsStaticDomain, Operation: pageDataRangeResetOperation,
		FieldMask: []string{}, OwnerSystemAccountIDs: []string{}, MembershipChanged: true,
		OrderChanged: true, FilterChanged: true, PageChanged: true, OccurredAt: "2026-07-18T01:02:03.456Z",
	}
	if err := publisher.Publish(t.Context(), event); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if capture.epochSets != 1 || capture.epochSetTTL != 0 || !capture.epochSetNX {
		t.Fatalf("epoch SET calls=%d ttl=%v nx=%v", capture.epochSets, capture.epochSetTTL, capture.epochSetNX)
	}
	if err := publisher.Publish(t.Context(), event); err != nil {
		t.Fatalf("second Publish() error = %v", err)
	}
	if capture.epochSets != 1 {
		t.Fatalf("epoch SET calls after cached publish = %d, want 1", capture.epochSets)
	}
}

func TestPageDataChangeLuaMatchesNodeV2RetentionContract(t *testing.T) {
	wants := []string{
		`redis.call('SISMEMBER', dedupeKey, eventId)`,
		`redis.call('SADD', dedupeKey, eventId)`,
		`redis.call('INCR', sequenceKey)`,
		`redis.call('RPUSH', logKey`,
		`redis.call('LTRIM', logKey, -maxEvents, -1)`,
		`redis.call('EXPIRE', logKey, ttlSeconds)`,
		`redis.call('EXPIRE', dedupeKey, ttlSeconds)`,
	}
	for _, want := range wants {
		if !strings.Contains(pageDataChangePublishLua, want) {
			t.Fatalf("page data change Lua missing %q", want)
		}
	}
	duplicateReturn := strings.Index(pageDataChangePublishLua, "return tonumber(redis.call('GET', sequenceKey) or '0')")
	firstExpire := strings.Index(pageDataChangePublishLua, "redis.call('EXPIRE'")
	if duplicateReturn < 0 || firstExpire < 0 || duplicateReturn > firstExpire {
		t.Fatal("duplicate eventId must return before any EXPIRE so it cannot renew TTL")
	}
	if strings.Contains(pageDataChangePublishLua, "EXPIRE', sequenceKey") {
		t.Fatal("sequence key must be persistent")
	}
}

func TestPageDataChangePublisherPreservesRedisAndJSONCauses(t *testing.T) {
	redisCause := errors.New("redis unavailable")
	publisher, _ := testPageDataChangePublisher()
	publisher.getEpoch = func(context.Context, string) (string, error) { return "", redisCause }
	event := PageDataChangeEvent{
		EventID: "event-1", Domain: pageDataAccountsStaticDomain, Operation: pageDataRangeResetOperation,
		FieldMask: []string{}, OwnerSystemAccountIDs: []string{}, MembershipChanged: true,
		OrderChanged: true, FilterChanged: true, PageChanged: true, OccurredAt: "2026-07-18T01:02:03.456Z",
	}
	if err := publisher.Publish(t.Context(), event); !errors.Is(err, redisCause) {
		t.Fatalf("Publish() error = %v, want Redis cause", err)
	}

	jsonCause := errors.New("json failed")
	publisher, _ = testPageDataChangePublisher()
	publisher.marshalEvent = func(PageDataChangeEvent) ([]byte, error) { return nil, jsonCause }
	if err := publisher.Publish(t.Context(), event); !errors.Is(err, jsonCause) {
		t.Fatalf("Publish() error = %v, want JSON cause", err)
	}
}

func TestPageDataChangePublisherRedisIntegration(t *testing.T) {
	rawURL := strings.TrimSpace(os.Getenv("JUHE_AI_TEST_REDIS_URL"))
	if rawURL == "" {
		if os.Getenv("JUHE_AI_REQUIRE_INTEGRATION") == "1" {
			t.Fatal("JUHE_AI_TEST_REDIS_URL is required when JUHE_AI_REQUIRE_INTEGRATION=1")
		}
		t.Skip("JUHE_AI_TEST_REDIS_URL is not configured")
	}

	namespace := "page-data-test-" + uuid.NewString()
	client, err := NewClient(rawURL, namespace+":state")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	if err := client.Ping(t.Context()); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	prefix := "juhe-ai:" + namespace + ":page-data-change:"
	t.Cleanup(func() {
		var cursor uint64
		for {
			keys, next, scanErr := client.client.Scan(context.Background(), cursor, prefix+"*", 128).Result()
			if scanErr != nil {
				t.Errorf("cleanup SCAN error = %v", scanErr)
				return
			}
			if len(keys) > 0 {
				if deleteErr := client.client.Del(context.Background(), keys...).Err(); deleteErr != nil {
					t.Errorf("cleanup DEL error = %v", deleteErr)
					return
				}
			}
			cursor = next
			if cursor == 0 {
				return
			}
		}
	})

	publisher, err := NewPageDataChangePublisher(client, namespace)
	if err != nil {
		t.Fatalf("NewPageDataChangePublisher() error = %v", err)
	}
	const concurrentEvents = 64
	var waitGroup sync.WaitGroup
	errorsCh := make(chan error, concurrentEvents)
	for index := 0; index < concurrentEvents; index++ {
		waitGroup.Add(1)
		go func(eventIndex int) {
			defer waitGroup.Done()
			errorsCh <- publisher.Publish(t.Context(), integrationPageDataEvent(eventIndex))
		}(index)
	}
	waitGroup.Wait()
	close(errorsCh)
	for publishErr := range errorsCh {
		if publishErr != nil {
			t.Fatalf("concurrent Publish() error = %v", publishErr)
		}
	}

	sequenceKey := prefix + "sequence:global:accounts.static"
	logKey := prefix + "log:global:accounts.static"
	dedupeKey := prefix + "dedupe:global:accounts.static"
	epochKey := prefix + "epoch:v2"
	if got, getErr := client.client.Get(t.Context(), sequenceKey).Int64(); getErr != nil || got != concurrentEvents {
		t.Fatalf("sequence after concurrency = %d, error = %v, want %d", got, getErr, concurrentEvents)
	}
	entries, err := client.client.LRange(t.Context(), logKey, 0, -1).Result()
	if err != nil {
		t.Fatalf("LRANGE after concurrency error = %v", err)
	}
	seenSequences := make(map[int64]struct{}, concurrentEvents)
	for _, entry := range entries {
		separator := strings.IndexByte(entry, '\t')
		if separator < 1 {
			t.Fatalf("invalid log entry %q", entry)
		}
		sequence, parseErr := strconv.ParseInt(entry[:separator], 10, 64)
		if parseErr != nil {
			t.Fatalf("parse log sequence %q: %v", entry[:separator], parseErr)
		}
		seenSequences[sequence] = struct{}{}
	}
	if len(seenSequences) != concurrentEvents {
		t.Fatalf("unique concurrent sequences = %d, want %d", len(seenSequences), concurrentEvents)
	}

	if err := publisher.Publish(t.Context(), integrationPageDataEvent(0)); err != nil {
		t.Fatalf("duplicate Publish() error = %v", err)
	}
	if got, getErr := client.client.Get(t.Context(), sequenceKey).Int64(); getErr != nil || got != concurrentEvents {
		t.Fatalf("sequence after duplicate = %d, error = %v, want %d", got, getErr, concurrentEvents)
	}
	for index := concurrentEvents; index < 300; index++ {
		if err := publisher.Publish(t.Context(), integrationPageDataEvent(index)); err != nil {
			t.Fatalf("Publish(%d) error = %v", index, err)
		}
	}
	if got, getErr := client.client.Get(t.Context(), sequenceKey).Int64(); getErr != nil || got != 300 {
		t.Fatalf("final sequence = %d, error = %v, want 300", got, getErr)
	}
	if got, llenErr := client.client.LLen(t.Context(), logKey).Result(); llenErr != nil || got != pageDataMaxEventOwners {
		t.Fatalf("trimmed log length = %d, error = %v, want %d", got, llenErr, pageDataMaxEventOwners)
	}
	for _, key := range []string{logKey, dedupeKey} {
		ttl, ttlErr := client.client.TTL(t.Context(), key).Result()
		if ttlErr != nil || ttl <= time.Duration(pageDataEventLogTTLSeconds-5)*time.Second || ttl > time.Duration(pageDataEventLogTTLSeconds)*time.Second {
			t.Fatalf("TTL(%q) = %v, error = %v", key, ttl, ttlErr)
		}
	}
	for _, key := range []string{epochKey, sequenceKey} {
		ttl, ttlErr := client.client.TTL(t.Context(), key).Result()
		if ttlErr != nil || ttl != -time.Nanosecond {
			t.Fatalf("persistent TTL(%q) = %v, error = %v", key, ttl, ttlErr)
		}
	}
	if epoch, getErr := client.client.Get(t.Context(), epochKey).Result(); getErr != nil || strings.TrimSpace(epoch) == "" {
		t.Fatalf("epoch = %q, error = %v", epoch, getErr)
	}
}

func integrationPageDataEvent(index int) PageDataChangeEvent {
	return PageDataChangeEvent{
		EventID: fmt.Sprintf("integration-event-%03d", index), Domain: pageDataAccountsStaticDomain,
		Operation: pageDataRangeResetOperation, FieldMask: []string{}, OwnerSystemAccountIDs: []string{},
		MembershipChanged: true, OrderChanged: true, FilterChanged: true, PageChanged: true,
		OccurredAt: "2026-07-18T01:02:03.456Z",
	}
}

type pageDataPublishCall struct {
	keys []string
	args []interface{}
}

type pageDataEpochCapture struct {
	epochGets   []string
	epochSets   int
	epochSetTTL time.Duration
	epochSetNX  bool
	epochValue  string
}

func testPageDataChangePublisher() (*PageDataChangePublisher, *pageDataEpochCapture) {
	capture := &pageDataEpochCapture{}
	publisher := &PageDataChangePublisher{
		keyPrefix:     "juhe-ai:prod-west:page-data-change",
		proposedEpoch: "epoch-1",
		now: func() time.Time {
			return time.Date(2026, 7, 18, 1, 2, 3, 456789000, time.UTC)
		},
	}
	nextID := 0
	publisher.newEventID = func() string {
		nextID++
		return fmt.Sprintf("event-%d", nextID)
	}
	publisher.getEpoch = func(_ context.Context, key string) (string, error) {
		capture.epochGets = append(capture.epochGets, key)
		return capture.epochValue, nil
	}
	publisher.setEpochNX = func(_ context.Context, _ string, value string, ttl time.Duration) (bool, error) {
		capture.epochSets++
		capture.epochSetNX = true
		capture.epochSetTTL = ttl
		capture.epochValue = value
		return true, nil
	}
	publisher.runPublish = func(context.Context, []string, ...interface{}) error { return nil }
	publisher.marshalEvent = func(event PageDataChangeEvent) ([]byte, error) {
		return json.Marshal(event)
	}
	return publisher, capture
}
