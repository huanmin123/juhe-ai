package runtimelogimport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
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
