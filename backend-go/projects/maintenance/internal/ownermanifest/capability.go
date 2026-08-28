package ownermanifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// CapabilityManifest is the domain-level handoff view layered on top of the
// operation manifest. It records the complete operation set for one
// transaction group and the evidence required before that group can move to
// the Gateway owner.
type CapabilityManifest struct {
	ManifestVersion int          `json:"manifest_version"`
	SourceManifest  string       `json:"source_manifest"`
	Capabilities    []Capability `json:"capabilities"`
}

type Capability struct {
	ID                       string   `json:"id"`
	NodeWriterOperationGroup string   `json:"node_writer_operation_group"`
	NodeOperations           []string `json:"node_operations"`
	OperationCount           int      `json:"operation_count"`
	CurrentOwner             string   `json:"current_owner"`
	TargetOwner              string   `json:"target_owner"`
	GatewayTargetModule      string   `json:"gateway_target_module"`
	Status                   string   `json:"status"`
	MigrationMethod          string   `json:"migration_method"`
	AcceptanceGates          []string `json:"acceptance_gates"`
	Rollback                 string   `json:"rollback"`
	Evidence                 []string `json:"evidence,omitempty"`
}

// CapabilityReport is intentionally machine-readable and suitable for CI
// evidence. A passing report proves only manifest completeness and internal
// consistency; it does not claim that any capability is production-ready.
type CapabilityReport struct {
	ManifestVersion int            `json:"manifestVersion"`
	Capabilities    int            `json:"capabilities"`
	Operations      int            `json:"operations"`
	Groups          int            `json:"groups"`
	StatusCoverage  map[string]int `json:"statusCoverage"`
}

var capabilityStatuses = map[string]struct{}{
	"implemented": {},
	"partial":     {},
	"missing":     {},
	// Excluded is an explicit non-handoff state for the chat/codex physical
	// store. It must never be interpreted as Go implementation evidence.
	"excluded": {},
}

// VerifyCapabilityManifest validates that every operation in the canonical
// Business SQLite manifest belongs to exactly one domain capability. It also
// fails closed on owner drift, stale counts, unsupported statuses, or absent
// acceptance/rollback metadata.
func VerifyCapabilityManifest(capabilityPath, operationPath string) (CapabilityReport, error) {
	var capabilities CapabilityManifest
	capabilityData, err := os.ReadFile(capabilityPath)
	if err != nil {
		return CapabilityReport{}, fmt.Errorf("read capability manifest: %w", err)
	}
	if err := json.Unmarshal(capabilityData, &capabilities); err != nil {
		return CapabilityReport{}, fmt.Errorf("decode capability manifest: %w", err)
	}
	if capabilities.ManifestVersion != 1 || len(capabilities.Capabilities) == 0 {
		return CapabilityReport{}, errors.New("capability manifest version or capabilities are invalid")
	}
	var operationsManifest manifest
	operationData, err := os.ReadFile(operationPath)
	if err != nil {
		return CapabilityReport{}, fmt.Errorf("read operation manifest: %w", err)
	}
	if err := json.Unmarshal(operationData, &operationsManifest); err != nil {
		return CapabilityReport{}, fmt.Errorf("decode operation manifest: %w", err)
	}
	if operationsManifest.ManifestVersion != 1 || len(operationsManifest.Operations) == 0 {
		return CapabilityReport{}, errors.New("operation manifest version or operations are invalid")
	}

	groups := make(map[string][]Operation)
	for _, operation := range operationsManifest.Operations {
		group := strings.TrimSpace(operation.Transaction)
		if group == "" {
			return CapabilityReport{}, fmt.Errorf("operation %q has an empty transaction group", operation.Operation)
		}
		groups[group] = append(groups[group], operation)
	}
	seenGroups := make(map[string]struct{}, len(capabilities.Capabilities))
	seenOperations := make(map[string]string, len(operationsManifest.Operations))
	report := CapabilityReport{
		ManifestVersion: capabilities.ManifestVersion,
		Capabilities:    len(capabilities.Capabilities),
		Operations:      len(operationsManifest.Operations),
		Groups:          len(groups),
		StatusCoverage:  make(map[string]int),
	}
	for _, capability := range capabilities.Capabilities {
		group := strings.TrimSpace(capability.NodeWriterOperationGroup)
		if group == "" || strings.TrimSpace(capability.ID) == "" {
			return CapabilityReport{}, errors.New("capability lacks id or Node operation group")
		}
		if _, duplicate := seenGroups[group]; duplicate {
			return CapabilityReport{}, fmt.Errorf("capability manifest contains duplicate group %q", group)
		}
		seenGroups[group] = struct{}{}
		groupOperations, ok := groups[group]
		if !ok {
			return CapabilityReport{}, fmt.Errorf("capability %q references unknown operation group %q", capability.ID, group)
		}
		if capability.OperationCount != len(groupOperations) {
			return CapabilityReport{}, fmt.Errorf("capability %q operation_count=%d, source group has %d", capability.ID, capability.OperationCount, len(groupOperations))
		}
		if capability.CurrentOwner == "" || capability.TargetOwner == "" || capability.GatewayTargetModule == "" || capability.MigrationMethod == "" || capability.Rollback == "" {
			return CapabilityReport{}, fmt.Errorf("capability %q lacks owner, module, migration, or rollback metadata", capability.ID)
		}
		if _, valid := capabilityStatuses[capability.Status]; !valid {
			return CapabilityReport{}, fmt.Errorf("capability %q has unsupported status %q", capability.ID, capability.Status)
		}
		if len(capability.AcceptanceGates) == 0 {
			return CapabilityReport{}, fmt.Errorf("capability %q has no acceptance gates", capability.ID)
		}
		if capability.Status == "implemented" && len(capability.Evidence) == 0 {
			return CapabilityReport{}, fmt.Errorf("capability %q claims implemented without evidence", capability.ID)
		}
		if capability.Status == "excluded" && capability.TargetOwner != "unchanged-excluded" {
			return CapabilityReport{}, fmt.Errorf("excluded capability %q must retain unchanged-excluded target owner", capability.ID)
		}
		if capability.Status != "excluded" && capability.TargetOwner != "go-gateway" && !(group == "read-only" && capability.TargetOwner == "read-consumer") {
			return CapabilityReport{}, fmt.Errorf("capability %q target owner %q is not an allowed handoff owner", capability.ID, capability.TargetOwner)
		}
		sourceOperations := make(map[string]Operation, len(groupOperations))
		for _, operation := range groupOperations {
			sourceOperations[operation.Operation] = operation
			if prior, duplicate := seenOperations[operation.Operation]; duplicate {
				return CapabilityReport{}, fmt.Errorf("operation %q is assigned to groups %q and %q", operation.Operation, prior, group)
			}
			seenOperations[operation.Operation] = group
			if operation.CurrentOwner != capability.CurrentOwner || operation.TargetOwner != capability.TargetOwner {
				return CapabilityReport{}, fmt.Errorf("capability %q owner metadata drifts from operation %q", capability.ID, operation.Operation)
			}
		}
		declared := make(map[string]struct{}, len(capability.NodeOperations))
		for _, operation := range capability.NodeOperations {
			name := strings.TrimSpace(operation)
			if name == "" {
				return CapabilityReport{}, fmt.Errorf("capability %q contains an empty operation", capability.ID)
			}
			if _, duplicate := declared[name]; duplicate {
				return CapabilityReport{}, fmt.Errorf("capability %q lists duplicate operation %q", capability.ID, name)
			}
			declared[name] = struct{}{}
			if _, exists := sourceOperations[name]; !exists {
				return CapabilityReport{}, fmt.Errorf("capability %q lists operation %q outside source group", capability.ID, name)
			}
		}
		if len(declared) != len(sourceOperations) {
			missing := make([]string, 0)
			for name := range sourceOperations {
				if _, exists := declared[name]; !exists {
					missing = append(missing, name)
				}
			}
			sort.Strings(missing)
			return CapabilityReport{}, fmt.Errorf("capability %q omits operations: %s", capability.ID, strings.Join(missing, ", "))
		}
		report.StatusCoverage[capability.Status]++
	}
	if len(seenGroups) != len(groups) {
		missing := make([]string, 0)
		for group := range groups {
			if _, exists := seenGroups[group]; !exists {
				missing = append(missing, group)
			}
		}
		sort.Strings(missing)
		return CapabilityReport{}, fmt.Errorf("capability manifest omits operation groups: %s", strings.Join(missing, ", "))
	}
	if len(seenOperations) != len(operationsManifest.Operations) {
		return CapabilityReport{}, fmt.Errorf("capability operation coverage=%d, source operations=%d", len(seenOperations), len(operationsManifest.Operations))
	}
	return report, nil
}
