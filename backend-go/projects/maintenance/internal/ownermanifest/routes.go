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
	// GatewayProof is required when the model-checks family claims complete
	// ownership. It records the two public management entrances, their scopes,
	// and the exact HTTP matrix that the Gateway handler must own. Keeping the
	// proof separate from NodeMutations matters because model-checks has reads
	// and SSE endpoints in addition to its eight mutations.
	GatewayProof *GatewayRouteProof `json:"gateway_proof,omitempty"`
}

type GatewayRouteProof struct {
	Mounts  []GatewayRouteMount `json:"mounts"`
	Methods []string            `json:"methods"`
}

type GatewayRouteMount struct {
	Path          string `json:"path"`
	Scope         string `json:"scope"`
	EvidenceFile  string `json:"evidence_file"`
	ScopeEvidence string `json:"scope_evidence"`
}

type GatewayRouteOwnerReport struct {
	ManifestVersion int            `json:"manifestVersion"`
	Families        int            `json:"families"`
	MutationRoutes  int            `json:"mutationRoutes"`
	StatusCoverage  map[string]int `json:"statusCoverage"`
	PendingFamilies []string       `json:"pendingFamilies,omitempty"`
}

var routeMethodPattern = regexp.MustCompile(`(?m)^\s*[A-Za-z0-9_]+\.(get|post|put|patch|delete)\(\s*'([^']*)'`)

var modelChecksRouteMatrix = []string{
	"POST /token-intercept-baselines/activate",
	"GET /options",
	"GET /quality-policy",
	"PATCH /quality-policy",
	"GET /quality-schedules",
	"POST /quality-schedules",
	"PATCH /quality-schedules/:id",
	"DELETE /quality-schedules/:id",
	"GET /account-options",
	"GET /options/accounts",
	"GET /run/active",
	"POST /run/stop",
	"POST /run",
	"POST /run/stream",
	"GET /runs",
	"GET /runs/:id",
}

var modelChecksGatewayHandlerNeedles = []string{
	`case r.Method == http.MethodPost && path == "/run":`,
	`case r.Method == http.MethodPost && path == "/token-intercept-baselines/activate":`,
	`case r.Method == http.MethodPost && path == "/run/stream":`,
	`case r.Method == http.MethodGet && path == "/run/active":`,
	`case r.Method == http.MethodPost && path == "/run/stop":`,
	`case r.Method == http.MethodGet && path == "/runs":`,
	`case r.Method == http.MethodGet && strings.HasPrefix(path, "/runs/"):`,
	`case r.Method == http.MethodGet && path == "/quality-policy":`,
	`case r.Method == http.MethodGet && path == "/options":`,
	`case r.Method == http.MethodGet && (path == "/account-options" || path == "/options/accounts"):`,
	`case r.Method == http.MethodPatch && path == "/quality-policy":`,
	`case r.Method == http.MethodGet && path == "/quality-schedules":`,
	`case r.Method == http.MethodPost && path == "/quality-schedules":`,
	`case r.Method == http.MethodPatch && strings.HasPrefix(path, "/quality-schedules/"):`,
	`case r.Method == http.MethodDelete && strings.HasPrefix(path, "/quality-schedules/"):`,
}

var routeStatuses = map[string]struct{}{"implemented": {}, "implemented-archive-pending": {}, "partial": {}, "missing": {}, "excluded": {}}

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
	// An app source inside final-archive is historical evidence, not a live
	// Node mount. Its app.use calls must still be parsed to prove the frozen
	// route inventory, but they cannot keep an owner gate open after archive.
	sourceAppArchived := strings.HasPrefix(filepath.ToSlash(manifest.SourceApp), "migration-backup/node/")
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
		if family.Status == "implemented-archive-pending" {
			if len(family.Evidence) == 0 {
				return GatewayRouteOwnerReport{}, fmt.Errorf("route family %q claims implemented-archive-pending without evidence", family.ID)
			}
			hasArchiveManifest := false
			for _, evidence := range family.Evidence {
				if strings.Contains(evidence, "final-archive") {
					hasArchiveManifest = true
					break
				}
			}
			if !hasArchiveManifest {
				return GatewayRouteOwnerReport{}, fmt.Errorf("route family %q archive-pending requires a final-archive manifest reference", family.ID)
			}
		}
		if err := verifyEvidenceFiles(repositoryRoot, family); err != nil {
			return GatewayRouteOwnerReport{}, err
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
		mounted := regexp.MustCompile(`(?m)^\s*app\.use\([^\n]*\b` + regexp.QuoteMeta(family.NodeRouterSymbol) + `\b`).Match(appSource)
		archivedSource := strings.HasPrefix(filepath.ToSlash(family.NodeRouterFile), "migration-backup/node/")
		if family.Status == "implemented-archive-pending" {
			// The slice is Go-owned: the Node mount MUST be gone, while the
			// physical file move is deferred to the P8 final archive. Once the
			// app source itself is archived, a preserved historical mount is not
			// a live mount and must not fail this read-only verifier.
			if mounted && !sourceAppArchived {
				return GatewayRouteOwnerReport{}, fmt.Errorf("route family %q is archive-pending but still mounted by %s", family.ID, family.NodeRouterFile)
			}
		} else if !sourceAppArchived && !mounted && !(family.ID == "model-checks" && ((family.Status == "partial") || (family.Status == "implemented" && archivedSource))) {
			return GatewayRouteOwnerReport{}, fmt.Errorf("route family %q router %q is not mounted by %s", family.ID, family.NodeRouterSymbol, manifest.SourceApp)
		}
		actual := make([]string, 0)
		for _, match := range routeMethodPattern.FindAllSubmatch(source, -1) {
			if string(match[0]) == "" {
				continue
			}
			// The regex intentionally matches the router symbol as a token; ensure
			// this family does not accidentally count another router in the file.
			prefix := []byte(family.NodeRouterSymbol + ".")
			if !strings.Contains(string(match[0]), string(prefix)) {
				continue
			}
			method := strings.ToUpper(string(match[1]))
			if method == "GET" {
				continue
			}
			actual = append(actual, method+" "+string(match[2]))
		}
		if len(actual) != family.MutationCount {
			return GatewayRouteOwnerReport{}, fmt.Errorf("route family %q mutation_count=%d, source has %d", family.ID, family.MutationCount, len(actual))
		}
		if len(family.NodeMutations) > 0 {
			if err := verifyMutationSet(family.ID, family.NodeMutations, actual); err != nil {
				return GatewayRouteOwnerReport{}, err
			}
		}
		if family.ID == "model-checks" {
			if err := verifyModelChecksRouteOwner(family, source, appSource, repositoryRoot); err != nil {
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

func verifyEvidenceFiles(repositoryRoot string, family GatewayRouteFamily) error {
	for _, evidence := range family.Evidence {
		if _, err := readRepositoryEvidence(repositoryRoot, evidence); err != nil {
			return fmt.Errorf("route family %q evidence %q is invalid: %w", family.ID, evidence, err)
		}
	}
	return nil
}

func readRepositoryEvidence(repositoryRoot, evidence string) ([]byte, error) {
	evidence = strings.TrimSpace(evidence)
	if evidence == "" {
		return nil, errors.New("path is empty")
	}
	if filepath.IsAbs(evidence) {
		return nil, errors.New("path must be repository-relative")
	}
	root, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root: %w", err)
	}
	path := filepath.Join(root, filepath.FromSlash(evidence))
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return nil, errors.New("path escapes repository root")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, errors.New("path is a directory")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, errors.New("file is empty")
	}
	return data, nil
}

func verifyModelChecksRouteOwner(family GatewayRouteFamily, source, appSource []byte, repositoryRoot string) error {
	if family.NodeMount != "/model-checks and /my-model-checks" {
		return fmt.Errorf("model-checks must declare both Node management entrances, got %q", family.NodeMount)
	}
	allNodeMethods := collectRouterMethods(source, family.NodeRouterSymbol)
	if err := verifyExactRouteSet("model-checks Node", modelChecksRouteMatrix, allNodeMethods); err != nil {
		return err
	}
	const selfMount = "app.use(`${systemApiPrefix}/my-model-checks`, forceSelfAccessScope, modelChecksRouter)"
	const adminMount = "app.use(`${systemApiPrefix}/model-checks`, requireAdmin, modelChecksRouter)"
	selfCount, adminCount := strings.Count(string(appSource), selfMount), strings.Count(string(appSource), adminMount)
	archivedSource := strings.HasPrefix(filepath.ToSlash(family.NodeRouterFile), "migration-backup/node/")
	if selfCount != 1 || adminCount != 1 {
		// During the staged handoff the Node mounts are intentionally removed;
		// the Gateway baseline below remains the source of truth until the
		// archived router is moved out of backend/src.
		if !((family.Status == "partial" || (family.Status == "implemented" && archivedSource)) && selfCount == 0 && adminCount == 0) {
			return errors.New("model-checks Node dual management mount or scope contract drifted")
		}
	}
	if err := verifyModelChecksGatewayBaseline(family, repositoryRoot); err != nil {
		return err
	}
	if family.Status == "implemented" {
		if err := verifyModelChecksGatewayProof(family.GatewayProof, repositoryRoot); err != nil {
			return err
		}
	}
	return nil
}

func collectRouterMethods(source []byte, routerSymbol string) []string {
	actual := make([]string, 0)
	for _, match := range routeMethodPattern.FindAllSubmatch(source, -1) {
		if !strings.Contains(string(match[0]), routerSymbol+".") {
			continue
		}
		actual = append(actual, strings.ToUpper(string(match[1]))+" "+string(match[2]))
	}
	return actual
}

func verifyExactRouteSet(label string, expected, actual []string) error {
	if len(actual) != len(expected) {
		return fmt.Errorf("%s route matrix count=%d, expected=%d", label, len(actual), len(expected))
	}
	return verifyMutationSet(label, expected, actual)
}

func verifyModelChecksGatewayBaseline(family GatewayRouteFamily, repositoryRoot string) error {
	handlerRoot := filepath.Join(repositoryRoot, filepath.FromSlash(family.GatewayHandler))
	info, err := os.Stat(handlerRoot)
	if err != nil {
		return fmt.Errorf("model-checks Gateway handler path: %w", err)
	}
	if !info.IsDir() {
		return errors.New("model-checks Gateway handler must be a directory")
	}
	httpEvidence := filepath.ToSlash(filepath.Join(family.GatewayHandler, "http.go"))
	foundHTTP := false
	for _, evidence := range family.Evidence {
		if filepath.ToSlash(evidence) == httpEvidence {
			foundHTTP = true
			break
		}
	}
	if !foundHTTP {
		return fmt.Errorf("model-checks evidence must include %q", httpEvidence)
	}
	handlerSource, err := readRepositoryEvidence(repositoryRoot, httpEvidence)
	if err != nil {
		return fmt.Errorf("read model-checks Gateway handler evidence: %w", err)
	}
	for _, needle := range modelChecksGatewayHandlerNeedles {
		if !strings.Contains(string(handlerSource), needle) {
			return fmt.Errorf("model-checks Gateway handler is missing route evidence %q", needle)
		}
	}
	mainSource, err := readRepositoryEvidence(repositoryRoot, "backend-go/projects/gateway/cmd/juhe-ai-gateway/main.go")
	if err != nil {
		return fmt.Errorf("read model-checks Gateway mount source: %w", err)
	}
	if !strings.Contains(string(mainSource), `j3bHost.Mount(managementMux, "/model-checks/")`) {
		return errors.New("model-checks partial Gateway mount evidence is missing")
	}
	return nil
}

func verifyModelChecksGatewayProof(proof *GatewayRouteProof, repositoryRoot string) error {
	if proof == nil {
		return errors.New("model-checks claims implemented without Gateway route proof")
	}
	if err := verifyExactRouteSet("model-checks Gateway", modelChecksRouteMatrix, proof.Methods); err != nil {
		return err
	}
	expectedScopes := map[string]string{
		"/__aisys__/api/model-checks":    "admin",
		"/__aisys__/api/my-model-checks": "self",
	}
	if len(proof.Mounts) != len(expectedScopes) {
		return fmt.Errorf("model-checks Gateway proof mounts=%d, expected=%d", len(proof.Mounts), len(expectedScopes))
	}
	seen := make(map[string]struct{}, len(proof.Mounts))
	for _, mount := range proof.Mounts {
		path := strings.TrimSpace(mount.Path)
		expectedScope, ok := expectedScopes[path]
		if !ok {
			return fmt.Errorf("model-checks Gateway proof has unexpected mount %q", mount.Path)
		}
		if _, duplicate := seen[path]; duplicate {
			return fmt.Errorf("model-checks Gateway proof duplicates mount %q", mount.Path)
		}
		seen[path] = struct{}{}
		if strings.TrimSpace(mount.Scope) != expectedScope {
			return fmt.Errorf("model-checks Gateway mount %q scope=%q, expected=%q", path, mount.Scope, expectedScope)
		}
		if strings.TrimSpace(mount.ScopeEvidence) == "" {
			return fmt.Errorf("model-checks Gateway mount %q scope evidence is empty", path)
		}
		evidence, err := readRepositoryEvidence(repositoryRoot, mount.EvidenceFile)
		if err != nil {
			return fmt.Errorf("model-checks Gateway mount %q evidence: %w", path, err)
		}
		if !strings.Contains(string(evidence), path) || !strings.Contains(string(evidence), mount.ScopeEvidence) {
			return fmt.Errorf("model-checks Gateway mount %q does not prove declared path and scope", path)
		}
	}
	return nil
}
