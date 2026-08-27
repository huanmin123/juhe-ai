package ownermanifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Operation is the machine-readable subset required to prove that every
// DbServiceOperation has an explicit handoff decision.
type Operation struct {
	Operation    string `json:"operation"`
	Access       string `json:"access"`
	Source       Source `json:"source"`
	Tables       string `json:"tables"`
	Transaction  string `json:"transaction_group"`
	CurrentOwner string `json:"current_owner"`
	TargetOwner  string `json:"target_owner"`
	Rollback     string `json:"rollback"`
	Verification string `json:"verification"`
}

// Source pins the exact TypeScript declaration and dispatch locations used
// to derive an operation. Line numbers are checked against the current source
// so a stale handoff manifest cannot silently survive a handler move.
type Source struct {
	TypeUnion      string `json:"type_union"`
	Handler        string `json:"handler"`
	TypeLine       int    `json:"type_line"`
	AccessModeLine int    `json:"access_mode_line"`
	HandlerLine    int    `json:"handler_line"`
	Entrypoint     string `json:"entrypoint"`
	WriterKind     string `json:"writer_kind"`
}

type manifest struct {
	ManifestVersion int         `json:"manifest_version"`
	Operations      []Operation `json:"operations"`
}

// Report is intentionally serializable so CI and release evidence can retain
// the exact operation counts without copying source text or secrets.
type Report struct {
	ManifestVersion   int `json:"manifestVersion"`
	Operations        int `json:"operations"`
	Writes            int `json:"writes"`
	Reads             int `json:"reads"`
	Maintenance       int `json:"maintenance"`
	Runtime           int `json:"runtime"`
	SourceMaintenance int `json:"sourceMaintenance"`
	SourceRuntime     int `json:"sourceRuntime"`
	HandlerMatches    int `json:"handlerMatches"`
}

var operationType = regexp.MustCompile(`(?m)^\s*\|\s*\{\s*type:\s*'([^']+)'`)
var accessType = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9_]+):\s*'(read|write|maintenance|runtime)'\s*,?\s*$`)

// This name existed in an older handler result contract but is no longer a
// DbServiceOperation. Keep it explicit so a stale source alias cannot be
// silently mistaken for a missing operation.
var legacyAccessAliases = map[string]struct{}{
	"project_account_health_jobs_outcome": {},
}

// Verify validates the handoff manifest against the TypeScript source of
// truth. It is deliberately read-only and fails closed on any omission or
// ambiguous owner decision.
func Verify(manifestPath, typesPath, accessPath, handlerPath string) (Report, error) {
	var value manifest
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return Report{}, fmt.Errorf("read owner manifest: %w", err)
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return Report{}, fmt.Errorf("decode owner manifest: %w", err)
	}
	if value.ManifestVersion != 1 || len(value.Operations) == 0 {
		return Report{}, errors.New("owner manifest version or operations are invalid")
	}
	typesData, err := os.ReadFile(typesPath)
	if err != nil {
		return Report{}, fmt.Errorf("read DbServiceOperation source: %w", err)
	}
	accessData, err := os.ReadFile(accessPath)
	if err != nil {
		return Report{}, fmt.Errorf("read DbServiceOperation access map: %w", err)
	}
	handlerData, err := os.ReadFile(handlerPath)
	if err != nil {
		return Report{}, fmt.Errorf("read DbServiceOperation handlers: %w", err)
	}

	typeBody := typesData
	start := strings.Index(string(typesData), "export type DbServiceOperation =")
	if start >= 0 {
		typeBody = typesData[start:]
		if end := strings.Index(string(typeBody), "\nexport type "); end > 0 {
			typeBody = typeBody[:end]
		}
	}
	types := make(map[string]struct{})
	for _, match := range operationType.FindAllSubmatch(typeBody, -1) {
		types[string(match[1])] = struct{}{}
	}
	if len(types) == 0 {
		return Report{}, errors.New("DbServiceOperation source contains no operation types")
	}
	access := make(map[string]string)
	for _, match := range accessType.FindAllSubmatch(accessData, -1) {
		access[string(match[1])] = string(match[2])
	}

	seen := make(map[string]struct{}, len(value.Operations))
	report := Report{ManifestVersion: value.ManifestVersion, Operations: len(value.Operations)}
	for _, item := range value.Operations {
		name := strings.TrimSpace(item.Operation)
		if name == "" {
			return Report{}, errors.New("owner manifest contains an empty operation")
		}
		if _, ok := seen[name]; ok {
			return Report{}, fmt.Errorf("owner manifest contains duplicate operation %q", name)
		}
		seen[name] = struct{}{}
		expectedAccess, ok := access[name]
		if !ok {
			return Report{}, fmt.Errorf("owner manifest operation %q is missing from access map", name)
		}
		if _, ok := types[name]; !ok {
			return Report{}, fmt.Errorf("owner manifest operation %q is missing from DbServiceOperation", name)
		}
		// The checked-in manifest intentionally folds all mutating modes
		// (write/maintenance/runtime) into the handoff class `write`; retain
		// the exact source mode in the access map while validating that coarse
		// owner decision.
		if expectedAccess == "read" {
			if item.Access != "read" {
				return Report{}, fmt.Errorf("operation %q access=%q, source=%q", name, item.Access, expectedAccess)
			}
		} else if expectedAccess == "runtime" && item.Access == "read" && item.TargetOwner == "read-consumer" {
			// `status` is classified as runtime by Node's dispatch table but
			// remains a read-only consumer in the handoff manifest.
		} else if item.Access != "write" {
			return Report{}, fmt.Errorf("operation %q mutating access=%q, source=%q", name, item.Access, expectedAccess)
		}
		if item.CurrentOwner == "" || item.TargetOwner == "" || item.Tables == "" || item.Transaction == "" || item.Rollback == "" || item.Verification == "" {
			return Report{}, fmt.Errorf("operation %q lacks owner, table, transaction, rollback, or verification metadata", name)
		}
		if item.Source.TypeUnion == "" || item.Source.Handler == "" || item.Source.TypeLine <= 0 || item.Source.AccessModeLine <= 0 || item.Source.HandlerLine <= 0 || item.Source.Entrypoint == "" || item.Source.WriterKind == "" {
			return Report{}, fmt.Errorf("operation %q lacks source location, entrypoint, or writer metadata", name)
		}
		if item.Source.TypeUnion != "backend/src/modules/db-service/db-service-types.ts" || item.Source.Handler != "backend/src/modules/db-service/db-service-handlers.ts" || item.Source.Entrypoint != "db-service-handler" {
			return Report{}, fmt.Errorf("operation %q has an unsupported source or entrypoint", name)
		}
		if item.Source.WriterKind != "business-writer" && item.Source.WriterKind != "read-consumer" {
			return Report{}, fmt.Errorf("operation %q has unsupported writer kind %q", name, item.Source.WriterKind)
		}
		if (item.Access == "read") != (item.Source.WriterKind == "read-consumer") {
			return Report{}, fmt.Errorf("operation %q access and writer kind disagree", name)
		}
		if !lineContains(typesData, item.Source.TypeLine, "type: '"+name+"'") {
			return Report{}, fmt.Errorf("operation %q type line %d is stale", name, item.Source.TypeLine)
		}
		if !lineContains(accessData, item.Source.AccessModeLine, name+":") {
			return Report{}, fmt.Errorf("operation %q access-mode line %d is stale", name, item.Source.AccessModeLine)
		}
		if !lineContains(handlerData, item.Source.HandlerLine, "'"+name+"'") {
			return Report{}, fmt.Errorf("operation %q handler line %d is stale", name, item.Source.HandlerLine)
		}
		if item.Access == "read" {
			report.Reads++
		} else if item.Access == "write" {
			report.Writes++
		}
		if expectedAccess == "maintenance" {
			report.SourceMaintenance++
		} else if expectedAccess == "runtime" {
			report.SourceRuntime++
		}
		if !bytesContainsHandler(handlerData, name) {
			return Report{}, fmt.Errorf("operation %q is not referenced by the handler source", name)
		}
		report.HandlerMatches++
	}
	if len(seen) != len(types) {
		missing := make([]string, 0)
		for name := range types {
			if _, ok := seen[name]; !ok {
				missing = append(missing, name)
			}
		}
		sort.Strings(missing)
		return Report{}, fmt.Errorf("owner manifest is missing DbServiceOperation entries: %s", strings.Join(missing, ", "))
	}
	for name := range access {
		if _, ok := types[name]; !ok {
			if _, legacy := legacyAccessAliases[name]; legacy {
				continue
			}
			return Report{}, fmt.Errorf("access map contains unknown operation %q", name)
		}
	}
	return report, nil
}

func bytesContainsHandler(source []byte, operation string) bool {
	needle := []byte("'" + operation + "'")
	if strings.Contains(string(source), string(needle)) {
		return true
	}
	// A few handlers switch on a typed value and use double quoted fixtures;
	// accepting the JSON spelling still requires the operation text to occur in
	// the handler source and does not make an absent handler pass.
	return strings.Contains(string(source), `"`+operation+`"`)
}

func lineContains(source []byte, line int, needle string) bool {
	if line <= 0 || needle == "" {
		return false
	}
	lines := strings.Split(string(source), "\n")
	return line <= len(lines) && strings.Contains(lines[line-1], needle)
}
