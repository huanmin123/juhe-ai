package runtimelogimport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const goldenRelativePath = "../../../../testdata/runtime-log-import-contract/v1/contract.json"

type contractGolden struct {
	Version              int                   `json:"version"`
	CapturedFrom         capturedFrom          `json:"capturedFrom"`
	Ownership            ownership             `json:"ownership"`
	Limits               limits                `json:"limits"`
	DiscoveryCases       []contractCase        `json:"discoveryCases"`
	CursorCases          []contractCase        `json:"cursorCases"`
	LineCases            []lineCase            `json:"lineCases"`
	TimestampCases       []contractCase        `json:"timestampCases"`
	ReplayCases          []contractCase        `json:"replayCases"`
	FacetCases           []contractCase        `json:"facetCases"`
	RotationRetention    []contractCase        `json:"rotationRetentionCases"`
	IndexEnabledCases    []contractCase        `json:"indexEnabledCases"`
	NodeDefects          []nodeDefect          `json:"nodeDefects"`
	GoRecommendations    []goRecommendation    `json:"goRecommendations"`
	ImplementationSlices []implementationSlice `json:"implementationSlices"`
}

type capturedFrom struct {
	Commit string   `json:"commit"`
	Owner  string   `json:"owner"`
	Files  []string `json:"files"`
}

type ownership struct {
	ProductionOwner string `json:"productionOwner"`
	GoStatus        string `json:"goStatus"`
	WriterCutover   bool   `json:"writerCutover"`
	SchemaChange    bool   `json:"schemaChange"`
}

type limits struct {
	PollIntervalMS          int `json:"pollIntervalMs"`
	MaxBytesPerFilePerPoll  int `json:"maxBytesPerFilePerPoll"`
	MaxLinesPerFilePerPoll  int `json:"maxLinesPerFilePerPoll"`
	BatchSize               int `json:"batchSize"`
	DiscoveryEntriesPerPoll int `json:"discoveryEntriesPerPoll"`
	DiscoveryFilesPerPoll   int `json:"discoveryFilesPerPoll"`
	RawJSONMaxBytes         int `json:"rawJsonMaxBytes"`
}

type contractCase struct {
	ID        string         `json:"id"`
	Principle string         `json:"principle"`
	Input     map[string]any `json:"input"`
	Expected  map[string]any `json:"expected"`
}

type lineCase struct {
	ID                  string         `json:"id"`
	Principle           string         `json:"principle"`
	SourceKey           string         `json:"sourceKey"`
	ExpectedStableID    string         `json:"expectedStableId"`
	Chunks              []string       `json:"chunks"`
	Expected            map[string]any `json:"expected"`
	GeneratedInputBytes int            `json:"generatedInputBytes,omitempty"`
}

type nodeDefect struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Observed string `json:"observed"`
	GoRule   string `json:"goRule"`
}

type goRecommendation struct {
	ID       string `json:"id"`
	Decision string `json:"decision"`
}

type implementationSlice struct {
	Order int      `json:"order"`
	ID    string   `json:"id"`
	Scope []string `json:"scope"`
}

func TestRuntimeLogImportContractGolden(t *testing.T) {
	golden := readGolden(t)

	if golden.Version != 1 {
		t.Fatalf("contract version = %d, want 1", golden.Version)
	}
	if golden.CapturedFrom.Owner != "node" || len(golden.CapturedFrom.Commit) != 40 {
		t.Fatalf("captured source = %#v, want immutable Node commit", golden.CapturedFrom)
	}
	if _, err := hex.DecodeString(golden.CapturedFrom.Commit); err != nil {
		t.Fatalf("captured commit is not hexadecimal: %v", err)
	}
	if golden.Ownership != (ownership{
		ProductionOwner: "node",
		GoStatus:        "contract_only",
		WriterCutover:   false,
		SchemaChange:    false,
	}) {
		t.Fatalf("ownership = %#v, contract fixture must not claim writer/schema ownership", golden.Ownership)
	}

	wantLimits := limits{
		PollIntervalMS:          1000,
		MaxBytesPerFilePerPoll:  1024 * 1024,
		MaxLinesPerFilePerPoll:  5000,
		BatchSize:               500,
		DiscoveryEntriesPerPoll: 2048,
		DiscoveryFilesPerPoll:   2048,
		RawJSONMaxBytes:         128 * 1024,
	}
	if golden.Limits != wantLimits {
		t.Fatalf("limits = %#v, want %#v", golden.Limits, wantLimits)
	}

	assertCaseIDs(t, "discovery", golden.DiscoveryCases, []string{
		"controlled-role-files-only",
		"rotated-before-current-stable-order",
		"bounded-continuation-no-starvation",
		"unrelated-entry-window-eventually-discovers",
		"exact-limit-reopens-at-eof",
	})
	assertCaseIDs(t, "cursor", golden.CursorCases, []string{
		"new-current-starts-at-eof",
		"new-rotated-starts-at-zero",
		"same-identity-growth-resumes",
		"same-identity-truncation-increments-generation",
		"rotation-relocates-identity-cursor",
		"path-replacement-displaces-old-identity",
		"cursor-error-keeps-file-pending",
		"cursor-completion-protects-rotated-file",
	})
	assertLineCases(t, golden.LineCases)
	assertCaseIDs(t, "timestamp", golden.TimestampCases, []string{
		"valid-time-normalizes-to-utc-milliseconds",
		"invalid-time-and-created-at-share-one-fallback-now",
	})
	assertCaseIDs(t, "replay", golden.ReplayCases, []string{
		"replay-is-idempotent-for-row-and-facets",
		"failed-batch-does-not-advance-cursor",
	})
	assertCaseIDs(t, "facet", golden.FacetCases, []string{
		"insert-increments-only-new-retained-rows",
		"retention-delete-decrements-and-recomputes-range",
	})
	assertCaseIDs(t, "rotation-retention", golden.RotationRetention, []string{
		"pending-or-error-rotated-file-cannot-be-deleted",
		"pending-without-error-cannot-be-deleted",
		"completed-with-error-cannot-be-deleted",
		"old-completed-error-free-cursor-can-be-cleaned",
		"rotation-handoff-preserves-unread-tail",
	})
	assertCaseIDs(t, "index-enabled", golden.IndexEnabledCases, []string{
		"enabled-runs-import-and-retention",
		"disabled-keeps-file-logging-and-grep-but-skips-index-work",
		"disabled-preserves-historical-index-and-allows-normal-rotation-cleanup",
	})

	assertNodeDefects(t, golden.NodeDefects)
	assertGoRecommendations(t, golden.GoRecommendations)
	assertImplementationSlices(t, golden.ImplementationSlices)
}

// These checks execute the fixture as a small reference model. They intentionally
// exercise only migration-contract semantics; production import code remains Node-owned.
func TestRuntimeLogImportContractExecutableFixtures(t *testing.T) {
	golden := readGolden(t)
	assertExecutableDiscoveryCases(t, golden.DiscoveryCases)
	assertExecutableCursorCases(t, golden.CursorCases)
	assertExecutableLineCases(t, golden.LineCases)
	assertExecutableTimestampCases(t, golden.TimestampCases)
	assertExecutableReplayAndFacetCases(t, golden.ReplayCases, golden.FacetCases)
	assertExecutableRotationRetentionCases(t, golden.RotationRetention)
}

func readGolden(t *testing.T) contractGolden {
	t.Helper()
	path := filepath.Clean(goldenRelativePath)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read read-only runtime log import golden %s: %v", path, err)
	}
	var golden contractGolden
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&golden); err != nil {
		t.Fatalf("decode runtime log import golden: %v", err)
	}
	return golden
}

func assertCaseIDs(t *testing.T, group string, cases []contractCase, want []string) {
	t.Helper()
	got := make([]string, 0, len(cases))
	for _, item := range cases {
		if strings.TrimSpace(item.Principle) == "" {
			t.Fatalf("%s case %q has no business principle", group, item.ID)
		}
		got = append(got, item.ID)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s case ids = %v, want %v", group, got, want)
	}
}

func assertLineCases(t *testing.T, cases []lineCase) {
	t.Helper()
	wantIDs := []string{
		"blank-complete-line-advances-without-row",
		"crlf-removes-only-trailing-carriage-return",
		"generation-zero-stable-id",
		"generation-positive-stable-id",
		"invalid-json-fallback-keeps-source",
		"newline-terminated-lines-only",
		"raw-json-hard-limit",
		"oversized-complete-line-progresses",
	}
	gotIDs := make([]string, 0, len(cases))
	for _, item := range cases {
		gotIDs = append(gotIDs, item.ID)
		if strings.TrimSpace(item.Principle) == "" {
			t.Fatalf("line case %q has no business principle", item.ID)
		}
		if item.SourceKey != "" {
			digest := sha256.Sum256([]byte(item.SourceKey))
			wantID := "rtlog_" + hex.EncodeToString(digest[:])[:32]
			if item.ExpectedStableID != wantID {
				t.Fatalf("line case %q stable id = %q, want %q", item.ID, item.ExpectedStableID, wantID)
			}
		}
		if item.ID == "newline-terminated-lines-only" {
			joined := strings.Join(item.Chunks, "")
			if !strings.HasSuffix(joined, `"event":"partial"}`) || strings.HasSuffix(joined, "\n") {
				t.Fatalf("partial-line fixture must end without newline: %q", joined)
			}
		}
		if item.ID == "raw-json-hard-limit" {
			if item.GeneratedInputBytes != 128*1024+1 {
				t.Fatalf("raw-json oversize input = %d, want one byte over limit", item.GeneratedInputBytes)
			}
			if item.Expected["storedMaxBytes"] != float64(128*1024) {
				t.Fatalf("raw-json storedMaxBytes = %#v", item.Expected["storedMaxBytes"])
			}
			if item.Expected["policy"] != "utf8_prefix_with_truncation_marker" {
				t.Fatalf("raw-json policy = %#v", item.Expected["policy"])
			}
		}
	}
	sort.Strings(gotIDs)
	sort.Strings(wantIDs)
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("line case ids = %v, want %v", gotIDs, wantIDs)
	}
}

func assertNodeDefects(t *testing.T, defects []nodeDefect) {
	t.Helper()
	want := map[string]string{
		"node-raw-json-limit-not-enforced": "confirmed",
		"node-batch-and-cursor-commit-gap": "confirmed",
	}
	if len(defects) != len(want) {
		t.Fatalf("node defects = %d, want %d", len(defects), len(want))
	}
	for _, defect := range defects {
		if want[defect.ID] != defect.Status || defect.Observed == "" || defect.GoRule == "" {
			t.Fatalf("invalid node defect: %#v", defect)
		}
	}
}

func assertGoRecommendations(t *testing.T, recommendations []goRecommendation) {
	t.Helper()
	want := []string{
		"single-owner-advisory-lock",
		"bounded-buffered-reader",
		"atomic-row-facet-cursor-transaction",
		"claim-retention-with-skip-locked",
	}
	got := make([]string, 0, len(recommendations))
	for _, recommendation := range recommendations {
		if strings.TrimSpace(recommendation.Decision) == "" {
			t.Fatalf("Go recommendation %q has no decision", recommendation.ID)
		}
		got = append(got, recommendation.ID)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Go recommendation ids = %v, want %v", got, want)
	}
}

func assertImplementationSlices(t *testing.T, slices []implementationSlice) {
	t.Helper()
	want := []string{"parser-and-bounded-reader", "transactional-import-store", "discovery-rotation-worker", "retention-and-owner-cutover"}
	if len(slices) != len(want) {
		t.Fatalf("implementation slices = %d, want %d", len(slices), len(want))
	}
	for index, slice := range slices {
		if slice.Order != index+1 || slice.ID != want[index] || len(slice.Scope) == 0 {
			t.Fatalf("implementation slice[%d] = %#v", index, slice)
		}
	}
}

func assertExecutableDiscoveryCases(t *testing.T, cases []contractCase) {
	t.Helper()
	roles := map[string]struct{}{
		"juhe-ai.log": {}, "juhe-ai.worker.log": {}, "juhe-ai.db-service.log": {},
		"juhe-ai.ingest-worker.log": {}, "juhe-ai.stats-worker.log": {},
		"juhe-ai.ops-worker.log": {}, "juhe-ai.temporary-maintenance-worker.log": {},
	}
	rotatedPattern := regexp.MustCompile(`^juhe-ai(?:\.[a-z-]+)?\.\d{8}T\d{6}Z\.[0-9a-f-]+\.log$`)
	for _, item := range cases {
		switch item.ID {
		case "controlled-role-files-only":
			for _, name := range stringSlice(t, item.Input, "recognizedCurrent") {
				if _, ok := roles[name]; !ok {
					t.Fatalf("unexpected recognized current file %q", name)
				}
			}
			if !rotatedPattern.MatchString(stringValue(t, item.Input, "recognizedRotated")) {
				t.Fatalf("recognized rotated file does not follow Node rotation name")
			}
			for _, name := range stringSlice(t, item.Input, "ignored") {
				if _, ok := roles[name]; ok || rotatedPattern.MatchString(name) {
					t.Fatalf("ignored file %q is recognized by the contract matcher", name)
				}
			}
		case "rotated-before-current-stable-order":
			kinds := stringSlice(t, item.Input, "kinds")
			sort.SliceStable(kinds, func(i, j int) bool { return kinds[i] == "rotated" && kinds[j] != "rotated" })
			if !reflect.DeepEqual(kinds, stringSlice(t, item.Expected, "kindOrder")) {
				t.Fatalf("discovery ordering = %v, want %v", kinds, item.Expected["kindOrder"])
			}
		case "bounded-continuation-no-starvation":
			matching := intValue(t, item.Input, "matchingFiles")
			first := min(matching, 2048)
			second := matching - first
			if first != intValue(t, item.Expected, "firstWindow") || second != intValue(t, item.Expected, "secondWindow") || first+second != intValue(t, item.Expected, "uniqueAcrossWindows") {
				t.Fatalf("discovery continuation = (%d, %d), want fixture result %#v", first, second, item.Expected)
			}
		case "unrelated-entry-window-eventually-discovers":
			entries := intValue(t, item.Input, "unrelatedLeading") + intValue(t, item.Input, "controlledTrailing")
			polls := (entries + 2048 - 1) / 2048
			if polls != intValue(t, item.Expected, "pollsUntilAllControlledFound") || !boolValue(t, item.Expected, "allControlledFound") {
				t.Fatalf("unrelated-entry discovery continuation = %d polls, want %#v", polls, item.Expected)
			}
		case "exact-limit-reopens-at-eof":
			matching := intValue(t, item.Input, "matchingFiles")
			if matching != 2048 || intValue(t, item.Expected, "firstWindow") != matching || intValue(t, item.Expected, "nextWindowAfterReopen") != matching || !boolValue(t, item.Expected, "emptyWindowForbidden") {
				t.Fatalf("exact-limit EOF fixture does not require immediate directory reopen: %#v", item)
			}
		}
	}
}

func assertExecutableCursorCases(t *testing.T, cases []contractCase) {
	t.Helper()
	for _, item := range cases {
		switch item.ID {
		case "new-current-starts-at-eof":
			if intValue(t, item.Input, "fileSize") != intValue(t, item.Expected, "cursorOffset") || intValue(t, item.Expected, "lineNumber") != 0 {
				t.Fatalf("new current cursor does not start at observed EOF: %#v", item)
			}
		case "new-rotated-starts-at-zero":
			if intValue(t, item.Expected, "cursorOffset") != 0 || intValue(t, item.Expected, "lineNumber") != 0 {
				t.Fatalf("new rotated cursor must start at zero: %#v", item)
			}
		case "same-identity-growth-resumes":
			if intValue(t, item.Input, "newFileSize") <= intValue(t, item.Input, "oldFileSize") || intValue(t, item.Expected, "cursorOffset") != intValue(t, item.Input, "cursorOffset") {
				t.Fatalf("growth cursor fixture is not resumable: %#v", item)
			}
		case "same-identity-truncation-increments-generation":
			if intValue(t, item.Input, "newFileSize") >= intValue(t, item.Input, "oldFileSize") || intValue(t, item.Expected, "cursorOffset") != 0 || intValue(t, item.Expected, "truncationGeneration") != intValue(t, item.Input, "truncationGeneration")+1 {
				t.Fatalf("truncation cursor fixture is not a reset transition: %#v", item)
			}
		case "rotation-relocates-identity-cursor":
			if intValue(t, item.Expected, "cursorOffset") != intValue(t, item.Input, "cursorOffset") || !boolValue(t, item.Expected, "identityPreserved") || !boolValue(t, item.Expected, "pathUpdated") {
				t.Fatalf("rotation relocation loses identity cursor: %#v", item)
			}
		case "path-replacement-displaces-old-identity":
			if stringValue(t, item.Input, "oldIdentity") == stringValue(t, item.Input, "newIdentity") || !boolValue(t, item.Expected, "oldCursorPreserved") || intValue(t, item.Expected, "replacementOffset") != 0 || intValue(t, item.Expected, "replacementGeneration") != 0 {
				t.Fatalf("path replacement fixture does not preserve old identity: %#v", item)
			}
		case "cursor-error-keeps-file-pending":
			if intValue(t, item.Expected, "cursorOffset") != intValue(t, item.Input, "flushedOffset") || boolValue(t, item.Expected, "completed") || !boolValue(t, item.Expected, "lastErrorPresent") {
				t.Fatalf("failed cursor fixture advances or completes: %#v", item)
			}
		case "cursor-completion-protects-rotated-file":
			complete := intValue(t, item.Input, "cursorOffset") >= intValue(t, item.Input, "fileSize") && item.Input["lastError"] == nil
			if !complete || !boolValue(t, item.Expected, "completed") || !boolValue(t, item.Expected, "safeForRotationDeletion") {
				t.Fatalf("completed cursor fixture is unsafe: %#v", item)
			}
		}
	}
}

func assertExecutableLineCases(t *testing.T, cases []lineCase) {
	t.Helper()
	for _, item := range cases {
		if item.ID == "raw-json-hard-limit" {
			raw := bytesRepeat([]byte{0xe7, 0x95, 0x8c}, 50000)
			stored := boundedUTF8(raw, intValue(t, item.Expected, "storedMaxBytes"))
			if len(stored) > intValue(t, item.Expected, "storedMaxBytes") || !utf8.Valid(stored) || !strings.HasSuffix(string(stored), "...[truncated]") {
				t.Fatalf("raw JSON bounding violates byte or UTF-8 boundary")
			}
			continue
		}
		if item.ID == "oversized-complete-line-progresses" {
			inputBytes := item.GeneratedInputBytes
			raw := bytesRepeat([]byte("x"), inputBytes)
			stored := boundedUTF8(raw, intValue(t, item.Expected, "storedMaxBytes"))
			if inputBytes <= 1024*1024 || intValue(t, item.Expected, "rowCount") != 1 || intValue(t, item.Expected, "cursorOffset") != inputBytes+1 || len(stored) > 128*1024 || !boolValue(t, item.Expected, "progresses") {
				t.Fatalf("oversized complete line does not progress with bounded storage: %#v", item)
			}
			continue
		}
		rows, lineNumber, cursorOffset, partial := executeLineChunks(item.Chunks)
		switch item.ID {
		case "newline-terminated-lines-only":
			if rows != intValue(t, item.Expected, "rowCount") || lineNumber != intValue(t, item.Expected, "lineNumber") || !partial || !boolValue(t, item.Expected, "partialRetained") {
				t.Fatalf("partial-line execution = rows=%d lines=%d partial=%v, want %#v", rows, lineNumber, partial, item.Expected)
			}
		case "crlf-removes-only-trailing-carriage-return":
			if rows != intValue(t, item.Expected, "rowCount") || cursorOffset != intValue(t, item.Expected, "cursorOffset") {
				t.Fatalf("CRLF execution = rows=%d offset=%d, want %#v", rows, cursorOffset, item.Expected)
			}
		case "blank-complete-line-advances-without-row":
			if rows != intValue(t, item.Expected, "rowCount") || lineNumber != intValue(t, item.Expected, "lineNumber") || cursorOffset != intValue(t, item.Expected, "cursorOffset") {
				t.Fatalf("blank-line execution = rows=%d lines=%d offset=%d, want %#v", rows, lineNumber, cursorOffset, item.Expected)
			}
		case "invalid-json-fallback-keeps-source":
			if rows != 1 || stringValue(t, item.Expected, "level") != "warn" || stringValue(t, item.Expected, "event") != "runtime_log_parse_failed" || !boolValue(t, item.Expected, "sourcePreserved") {
				t.Fatalf("invalid JSON fixture does not model searchable fallback: %#v", item)
			}
		}
	}
}

func assertExecutableTimestampCases(t *testing.T, cases []contractCase) {
	t.Helper()
	for _, item := range cases {
		switch item.ID {
		case "valid-time-normalizes-to-utc-milliseconds":
			got := make([]string, 0)
			for _, value := range stringSlice(t, item.Input, "values") {
				got = append(got, normalizeFixtureTime(t, value).Format("2006-01-02T15:04:05.000Z"))
			}
			if !reflect.DeepEqual(got, stringSlice(t, item.Expected, "values")) {
				t.Fatalf("timestamp normalization = %v, want %#v", got, item.Expected["values"])
			}
		case "invalid-time-and-created-at-share-one-fallback-now":
			fallback := time.Date(2026, 7, 22, 1, 2, 3, 456000000, time.UTC).Format("2006-01-02T15:04:05.000Z")
			if !boolValue(t, item.Expected, "sameFallback") || !boolValue(t, item.Expected, "utcMilliseconds") || fallback != "2026-07-22T01:02:03.456Z" {
				t.Fatalf("invalid timestamp fallback is not a shared UTC millisecond value")
			}
		}
	}
}

func assertExecutableReplayAndFacetCases(t *testing.T, replay, facets []contractCase) {
	t.Helper()
	for _, item := range replay {
		if item.ID != "replay-is-idempotent-for-row-and-facets" {
			continue
		}
		seen := map[string]bool{}
		rowCount, facetCount := 0, 0
		for attempt := 0; attempt < intValue(t, item.Input, "attempts"); attempt++ {
			id := stringValue(t, item.Input, "stableId")
			if !seen[id] {
				seen[id] = true
				rowCount++
				facetCount++
			}
		}
		if rowCount != intValue(t, item.Expected, "rows") || facetCount != intValue(t, item.Expected, "summaryCount") || facetCount != intValue(t, item.Expected, "levelCount") || facetCount != intValue(t, item.Expected, "eventCount") {
			t.Fatalf("idempotent replay failed fixture: %#v", item)
		}
	}
	for _, item := range facets {
		switch item.ID {
		case "insert-increments-only-new-retained-rows":
			if intValue(t, item.Expected, "summaryIncrement") != intValue(t, item.Input, "newRetainedRows") || intValue(t, item.Expected, "levelIncrement") != intValue(t, item.Input, "newRetainedRows") || intValue(t, item.Expected, "eventIncrement") != 1 {
				t.Fatalf("facet insert fixture includes duplicate or expired rows: %#v", item)
			}
		case "retention-delete-decrements-and-recomputes-range":
			if intValue(t, item.Expected, "summaryCount") != intValue(t, item.Input, "remainingRows") || !boolValue(t, item.Expected, "zeroBucketsRemoved") || !boolValue(t, item.Expected, "rangeRecomputed") {
				t.Fatalf("facet retention fixture does not recompute retained state: %#v", item)
			}
		}
	}
}

func assertExecutableRotationRetentionCases(t *testing.T, cases []contractCase) {
	t.Helper()
	for _, item := range cases {
		switch item.ID {
		case "pending-or-error-rotated-file-cannot-be-deleted", "pending-without-error-cannot-be-deleted", "completed-with-error-cannot-be-deleted":
			lastError, _ := item.Input["lastError"].(string)
			complete := intValue(t, item.Input, "cursorOffset") >= intValue(t, item.Input, "fileSize") && lastError == ""
			if complete || boolValue(t, item.Expected, "fileDeleteAllowed") || boolValue(t, item.Expected, "cursorCleanupAllowed") {
				t.Fatalf("pending rotated file became deletable: %#v", item)
			}
		case "old-completed-error-free-cursor-can-be-cleaned":
			complete := intValue(t, item.Input, "cursorOffset") >= intValue(t, item.Input, "fileSize") && item.Input["lastError"] == nil
			if !complete || !boolValue(t, item.Input, "olderThanCutoff") || !boolValue(t, item.Expected, "cursorCleanupAllowed") {
				t.Fatalf("completed old cursor is not cleanable: %#v", item)
			}
		case "rotation-handoff-preserves-unread-tail":
			if intValue(t, item.Expected, "rotatedReadStart") != intValue(t, item.Input, "oldCursorOffset") || intValue(t, item.Expected, "newCurrentInitialOffset") != intValue(t, item.Input, "newCurrentFileSize") || !reflect.DeepEqual(stringSlice(t, item.Expected, "order"), []string{"rotated", "current"}) {
				t.Fatalf("rotation handoff fixture loses old tail or wrong order: %#v", item)
			}
		}
	}
}

func executeLineChunks(chunks []string) (rows, lineNumber, cursorOffset int, partial bool) {
	data := []byte(strings.Join(chunks, ""))
	start := 0
	for index, value := range data {
		if value != '\n' {
			continue
		}
		line := data[start:index]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		lineNumber++
		cursorOffset = index + 1
		if strings.TrimSpace(string(line)) != "" {
			rows++
		}
		start = index + 1
	}
	return rows, lineNumber, cursorOffset, start < len(data)
}

func boundedUTF8(raw []byte, max int) []byte {
	if len(raw) <= max {
		return raw
	}
	marker := []byte("...[truncated]")
	end := max - len(marker)
	for end > 0 && !utf8.Valid(raw[:end]) {
		end--
	}
	return append(append([]byte{}, raw[:end]...), marker...)
}

func bytesRepeat(value []byte, count int) []byte { return []byte(strings.Repeat(string(value), count)) }

func normalizeFixtureTime(t *testing.T, value string) time.Time {
	t.Helper()
	for _, layout := range []string{time.RFC3339Nano, time.RFC1123} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	t.Fatalf("fixture time %q cannot be parsed", value)
	return time.Time{}
}

func stringSlice(t *testing.T, values map[string]any, key string) []string {
	t.Helper()
	items, ok := values[key].([]any)
	if !ok {
		t.Fatalf("fixture %q = %#v, want string array", key, values[key])
	}
	result := make([]string, len(items))
	for index, value := range items {
		result[index] = value.(string)
	}
	return result
}

func stringValue(t *testing.T, values map[string]any, key string) string {
	t.Helper()
	value, ok := values[key].(string)
	if !ok {
		t.Fatalf("fixture %q = %#v, want string", key, values[key])
	}
	return value
}

func intValue(t *testing.T, values map[string]any, key string) int {
	t.Helper()
	value, ok := values[key].(float64)
	if !ok {
		t.Fatalf("fixture %q = %#v, want number", key, values[key])
	}
	return int(value)
}

func boolValue(t *testing.T, values map[string]any, key string) bool {
	t.Helper()
	value, ok := values[key].(bool)
	if !ok {
		t.Fatalf("fixture %q = %#v, want boolean", key, values[key])
	}
	return value
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
