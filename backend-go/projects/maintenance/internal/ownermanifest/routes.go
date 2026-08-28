package ownermanifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// GatewayRouteOwnerManifest records every mutating router mounted by the
// Node system-api and the future in-process Gateway handler that must replace
// it. It is deliberately family-scoped: route declarations are counted from
// the pinned TypeScript source, so adding a new mutation without updating this
// manifest fails closed.
type GatewayRouteOwnerManifest struct {
	ManifestVersion int                  `json:"manifest_version"`
	SourceApp       string               `json:"source_app"`
	Families        []GatewayRouteFamily `json:"families"`
}

type GatewayRouteFamily struct {
	ID               string   `json:"id"`
	NodeMount        string   `json:"node_mount"`
	NodeRouterFile   string   `json:"node_router_file"`
	NodeRouterSymbol string   `json:"node_router_symbol"`
	NodeMutations    []string `json:"node_mutations"`
	MutationCount    int      `json:"mutation_count"`
	GatewayHandler   string   `json:"gateway_handler"`
	Status           string   `json:"status"`
	AcceptanceGates  []string `json:"acceptance_gates"`
	Rollback         string   `json:"rollback"`
	Evidence         []string `json:"evidence,omitempty"`
}

type GatewayRouteOwnerReport struct {
	ManifestVersion int            `json:"manifestVersion"`
	Families        int            `json:"families"`
	MutationRoutes  int            `json:"mutationRoutes"`
	StatusCoverage  map[string]int `json:"statusCoverage"`
	PendingFamilies []string       `json:"pendingFamilies,omitempty"`
}

var mutationRoutePattern = regexp.MustCompile(`(?m)^\s*[A-Za-z0-9_]+\.(post|put|patch|delete)\(\s*'([^']*)'`)

var routeStatuses = map[string]struct{}{"implemented": {}, "partial": {}, "missing": {}, "excluded": {}}

// VerifyGatewayRouteOwnerManifest validates source coverage and ownership
// metadata. A structurally valid manifest still exits as pending when any
// route family is partial/missing; callers must treat that as a closed gate.
func VerifyGatewayRouteOwnerManifest(manifestPath, repositoryRoot string) (GatewayRouteOwnerReport, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return GatewayRouteOwnerReport{}, fmt.Errorf("read gateway route manifest: %w", err)
	}
	var manifest GatewayRouteOwnerManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return GatewayRouteOwnerReport{}, fmt.Errorf("decode gateway route manifest: %w", err)
	}
	if manifest.ManifestVersion != 1 || len(manifest.Families) == 0 {
		return GatewayRouteOwnerReport{}, errors.New("gateway route manifest version or families are invalid")
	}
	if strings.TrimSpace(manifest.SourceApp) == "" {
		return GatewayRouteOwnerReport{}, errors.New("gateway route manifest source app is empty")
	}
	appSource, err := os.ReadFile(filepath.Join(repositoryRoot, filepath.FromSlash(manifest.SourceApp)))
	if err != nil {
		return GatewayRouteOwnerReport{}, fmt.Errorf("read Node system-api app: %w", err)
	}
	report := GatewayRouteOwnerReport{ManifestVersion: manifest.ManifestVersion, Families: len(manifest.Families), StatusCoverage: map[string]int{}}
	seenIDs := map[string]struct{}{}
	seenFiles := map[string]struct{}{}
	for _, family := range manifest.Families {
		if strings.TrimSpace(family.ID) == "" || strings.TrimSpace(family.NodeMount) == "" || strings.TrimSpace(family.NodeRouterFile) == "" || strings.TrimSpace(family.NodeRouterSymbol) == "" || strings.TrimSpace(family.GatewayHandler) == "" || strings.TrimSpace(family.Rollback) == "" {
			return GatewayRouteOwnerReport{}, fmt.Errorf("route family lacks identity, source, handler, or rollback metadata")
		}
		if _, ok := seenIDs[family.ID]; ok {
			return GatewayRouteOwnerReport{}, fmt.Errorf("duplicate route family %q", family.ID)
		}
		seenIDs[family.ID] = struct{}{}
		if _, ok := routeStatuses[family.Status]; !ok {
			return GatewayRouteOwnerReport{}, fmt.Errorf("route family %q has unsupported status %q", family.ID, family.Status)
		}
		if len(family.AcceptanceGates) == 0 {
			return GatewayRouteOwnerReport{}, fmt.Errorf("route family %q has no acceptance gates", family.ID)
		}
		if family.Status == "implemented" && len(family.Evidence) == 0 {
			return GatewayRouteOwnerReport{}, fmt.Errorf("route family %q claims implemented without evidence", family.ID)
		}
		if family.NodeRouterFile == "" {
			return GatewayRouteOwnerReport{}, fmt.Errorf("route family %q has empty source file", family.ID)
		}
		rel := filepath.FromSlash(family.NodeRouterFile)
		if _, ok := seenFiles[rel]; ok {
			return GatewayRouteOwnerReport{}, fmt.Errorf("source router file %q is assigned more than once", family.NodeRouterFile)
		}
		seenFiles[rel] = struct{}{}
		source, err := os.ReadFile(filepath.Join(repositoryRoot, rel))
		if err != nil {
			return GatewayRouteOwnerReport{}, fmt.Errorf("read route family %q source: %w", family.ID, err)
		}
		if !regexp.MustCompile(`(?m)^\s*app\.use\([^\n]*\b` + regexp.QuoteMeta(family.NodeRouterSymbol) + `\b`).Match(appSource) {
			return GatewayRouteOwnerReport{}, fmt.Errorf("route family %q router %q is not mounted by %s", family.ID, family.NodeRouterSymbol, manifest.SourceApp)
		}
		actual := make([]string, 0)
		for _, match := range mutationRoutePattern.FindAllSubmatch(source, -1) {
			if string(match[0]) == "" {
				continue
			}
			// The regex intentionally matches the router symbol as a token; ensure
			// this family does not accidentally count another router in the file.
			prefix := []byte(family.NodeRouterSymbol + ".")
			if !strings.Contains(string(match[0]), string(prefix)) {
				continue
			}
			actual = append(actual, strings.ToUpper(string(match[1]))+" "+string(match[2]))
		}
		if len(actual) != family.MutationCount {
			return GatewayRouteOwnerReport{}, fmt.Errorf("route family %q mutation_count=%d, source has %d", family.ID, family.MutationCount, len(actual))
		}
		if len(family.NodeMutations) > 0 {
			if err := verifyMutationSet(family.ID, family.NodeMutations, actual); err != nil {
				return GatewayRouteOwnerReport{}, err
			}
		}
		report.MutationRoutes += len(actual)
		report.StatusCoverage[family.Status]++
		if family.Status != "implemented" && family.Status != "excluded" {
			report.PendingFamilies = append(report.PendingFamilies, family.ID)
		}
	}
	sort.Strings(report.PendingFamilies)
	return report, nil
}

func verifyMutationSet(id string, declared, actual []string) error {
	if len(declared) != len(actual) {
		return fmt.Errorf("route family %q declares %d mutations, source has %d", id, len(declared), len(actual))
	}
	declaredSet := make(map[string]struct{}, len(declared))
	for _, item := range declared {
		item = strings.TrimSpace(item)
		if item == "" {
			return fmt.Errorf("route family %q declares an empty mutation", id)
		}
		if _, duplicate := declaredSet[item]; duplicate {
			return fmt.Errorf("route family %q declares duplicate mutation %q", id, item)
		}
		declaredSet[item] = struct{}{}
	}
	actualSet := make(map[string]struct{}, len(actual))
	for _, item := range actual {
		actualSet[item] = struct{}{}
	}
	missing, unexpected := make([]string, 0), make([]string, 0)
	for item := range declaredSet {
		if _, ok := actualSet[item]; !ok {
			missing = append(missing, item)
		}
	}
	for item := range actualSet {
		if _, ok := declaredSet[item]; !ok {
			unexpected = append(unexpected, item)
		}
	}
	if len(missing) == 0 && len(unexpected) == 0 {
		return nil
	}
	sort.Strings(missing)
	sort.Strings(unexpected)
	return fmt.Errorf("route family %q mutation contract drift: missing=%s unexpected=%s", id, strings.Join(missing, ","), strings.Join(unexpected, ","))
}
