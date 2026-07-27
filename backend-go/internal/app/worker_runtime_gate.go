package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/ownerlock"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
	"juhe-ai/backend-go/internal/version"
)

const workerRuntimeGateTimeout = 5 * time.Second

type WorkerRunner func(context.Context) error

type workerRuntimeLock interface {
	Release() error
}

type workerRuntimeSchemaStore interface {
	Ping(context.Context) error
	RequireGooseSchemaVersion(context.Context, int64) error
	Close()
}

type workerRuntimeGateDependencies struct {
	readManifest func(string) (workerOwnerManifest, error)
	acquire      func(string, ownerlock.Metadata) (workerRuntimeLock, error)
	openStore    func(context.Context, string) (workerRuntimeSchemaStore, error)
}

type workerOwnerManifest struct {
	SchemaVersion   int    `json:"schemaVersion"`
	DeploymentEpoch string `json:"deploymentEpoch"`
	Release         struct {
		NodeVersion   string `json:"nodeVersion"`
		GoVersion     string `json:"goVersion"`
		SchemaVersion int64  `json:"schemaVersion"`
	} `json:"release"`
	RouteOwners struct {
		Management string `json:"management"`
		Public     string `json:"public"`
		Gateway    string `json:"gateway"`
		Worker     string `json:"worker"`
	} `json:"routeOwners"`
	RollbackRouteOwners struct {
		Management string `json:"management"`
		Public     string `json:"public"`
		Gateway    string `json:"gateway"`
		Worker     string `json:"worker"`
	} `json:"rollbackRouteOwners"`
	RouteAllowlist []workerOwnerRoute `json:"routeAllowlist"`
}

type workerOwnerRoute struct {
	Surface       string `json:"surface"`
	Method        string `json:"method"`
	Path          string `json:"path"`
	Owner         string `json:"owner"`
	RollbackOwner string `json:"rollbackOwner"`
}

var (
	workerOwnerParameterSegmentPattern       = regexp.MustCompile(`^\{([A-Za-z][A-Za-z0-9_]*)\}$`)
	workerOwnerActionParameterSegmentPattern = regexp.MustCompile(`^\{([A-Za-z][A-Za-z0-9_]*)\}(:[A-Za-z][A-Za-z0-9._~-]*)$`)
	workerOwnerLiteralSegmentPattern         = regexp.MustCompile(`^[A-Za-z0-9._~:-]+$`)
)

func RunWorkerWithRuntimeGate(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	runner WorkerRunner,
) error {
	return runWorkerWithRuntimeGate(ctx, cfg, logger, runner, workerRuntimeGateDependencies{
		readManifest: readWorkerOwnerManifest,
		acquire: func(path string, metadata ownerlock.Metadata) (workerRuntimeLock, error) {
			return ownerlock.Acquire(path, metadata)
		},
		openStore: func(ctx context.Context, rawURL string) (workerRuntimeSchemaStore, error) {
			return postgresstore.Open(ctx, rawURL)
		},
	})
}

func runWorkerWithRuntimeGate(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	runner WorkerRunner,
	deps workerRuntimeGateDependencies,
) error {
	if runner == nil {
		return fmt.Errorf("worker runner is required")
	}
	if !cfg.OwnerLockEnabled {
		return fmt.Errorf("Go mutating worker requires JUHE_AI_OWNER_LOCK_ENABLED=true")
	}
	if strings.TrimSpace(cfg.OwnerLockRole) != "worker" {
		return fmt.Errorf("Go worker owner lock role must be worker")
	}
	if !cfg.GoWorkerExclusiveOwner {
		return fmt.Errorf("Go mutating worker requires JUHE_AI_GO_WORKER_EXCLUSIVE_OWNER=true")
	}
	if !cfg.LegacyNodeWorkerDrained {
		return fmt.Errorf("Go mutating worker requires JUHE_AI_LEGACY_NODE_WORKER_DRAINED=true")
	}
	if !filepath.IsAbs(strings.TrimSpace(cfg.OwnerLockPath)) {
		return fmt.Errorf("Go worker owner lock path must be absolute")
	}
	manifestPath := strings.TrimSpace(cfg.OwnerManifestPath)
	if !filepath.IsAbs(manifestPath) {
		return fmt.Errorf("Go worker owner manifest path must be absolute")
	}
	readManifest := deps.readManifest
	if readManifest == nil {
		readManifest = readWorkerOwnerManifest
	}
	manifest, err := readManifest(manifestPath)
	if err != nil {
		return fmt.Errorf("read worker owner manifest: %w", err)
	}
	if err := validateWorkerOwnerManifest(manifest, cfg); err != nil {
		return err
	}
	if logger == nil {
		logger = slog.Default()
	}

	runtimeLock, err := deps.acquire(cfg.OwnerLockPath, ownerlock.Metadata{
		DeploymentEpoch: cfg.OwnerLockDeploymentEpoch,
		RouteOwner:      "worker",
		Version:         version.Version,
		PID:             os.Getpid(),
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := runtimeLock.Release(); err != nil {
			logger.Error("释放 Go worker owner lock 失败", slog.String("error", err.Error()))
		}
	}()

	store, err := deps.openStore(ctx, cfg.PostgresURL)
	if err != nil {
		return fmt.Errorf("open worker schema gate PostgreSQL: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, workerRuntimeGateTimeout)
	err = store.Ping(pingCtx)
	cancel()
	if err != nil {
		store.Close()
		return fmt.Errorf("ping worker schema gate PostgreSQL: %w", err)
	}
	schemaCtx, cancel := context.WithTimeout(ctx, workerRuntimeGateTimeout)
	err = store.RequireGooseSchemaVersion(schemaCtx, version.SchemaVersion)
	cancel()
	store.Close()
	if err != nil {
		return fmt.Errorf("require goose schema version %d: %w", version.SchemaVersion, err)
	}

	return runner(ctx)
}

func readWorkerOwnerManifest(path string) (workerOwnerManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return workerOwnerManifest{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest workerOwnerManifest
	if err := decoder.Decode(&manifest); err != nil {
		return workerOwnerManifest{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return workerOwnerManifest{}, fmt.Errorf("owner manifest contains multiple JSON values")
		}
		return workerOwnerManifest{}, err
	}
	return manifest, nil
}

func validateWorkerOwnerManifest(manifest workerOwnerManifest, cfg config.Config) error {
	if manifest.SchemaVersion != 2 {
		return fmt.Errorf("worker owner manifest schemaVersion must be 2")
	}
	if strings.TrimSpace(manifest.DeploymentEpoch) == "" {
		return fmt.Errorf("worker owner manifest deployment epoch must be non-empty")
	}
	if manifest.DeploymentEpoch != strings.TrimSpace(cfg.OwnerLockDeploymentEpoch) {
		return fmt.Errorf("worker owner manifest deployment epoch does not match owner lock epoch")
	}
	if strings.TrimSpace(manifest.Release.NodeVersion) == "" {
		return fmt.Errorf("worker owner manifest Node version must be non-empty")
	}
	if manifest.RouteOwners.Worker != "go" {
		return fmt.Errorf("worker owner manifest must declare routeOwners.worker=go")
	}
	if manifest.Release.GoVersion != version.Version {
		return fmt.Errorf("worker owner manifest Go version must be %s", version.Version)
	}
	if manifest.Release.SchemaVersion != version.SchemaVersion {
		return fmt.Errorf("worker owner manifest schema version must be %d", version.SchemaVersion)
	}
	if err := validateWorkerOwnerMap("routeOwners", manifest.RouteOwners.Management, manifest.RouteOwners.Public, manifest.RouteOwners.Gateway, manifest.RouteOwners.Worker); err != nil {
		return err
	}
	if err := validateWorkerOwnerMap("rollbackRouteOwners", manifest.RollbackRouteOwners.Management, manifest.RollbackRouteOwners.Public, manifest.RollbackRouteOwners.Gateway, manifest.RollbackRouteOwners.Worker); err != nil {
		return err
	}
	if manifest.RouteAllowlist == nil {
		return fmt.Errorf("worker owner manifest routeAllowlist is required")
	}
	if len(manifest.RouteAllowlist) > 2048 {
		return fmt.Errorf("worker owner manifest routeAllowlist must contain at most 2048 routes")
	}
	parsedRoutes := make([]workerOwnerParsedRoute, 0, len(manifest.RouteAllowlist))
	for index, route := range manifest.RouteAllowlist {
		parsed, err := validateWorkerOwnerRoute(index, route)
		if err != nil {
			return err
		}
		parsedRoutes = append(parsedRoutes, parsed)
	}
	for leftIndex, left := range parsedRoutes {
		for rightIndex := leftIndex + 1; rightIndex < len(parsedRoutes); rightIndex++ {
			right := parsedRoutes[rightIndex]
			if left.method == right.method && workerOwnerPathTemplatesOverlap(left.segments, right.segments) {
				return fmt.Errorf("worker owner manifest routeAllowlist[%d] overlaps routeAllowlist[%d]", left.index, right.index)
			}
		}
	}
	return nil
}

func validateWorkerOwnerMap(label, management, public, gateway, worker string) error {
	for name, owner := range map[string]string{
		"management": management,
		"public":     public,
		"gateway":    gateway,
		"worker":     worker,
	} {
		if owner != "node" && owner != "go" {
			return fmt.Errorf("worker owner manifest %s.%s must be node or go", label, name)
		}
	}
	return nil
}

type workerOwnerParsedRoute struct {
	index    int
	method   string
	segments []string
}

func validateWorkerOwnerRoute(index int, route workerOwnerRoute) (workerOwnerParsedRoute, error) {
	label := fmt.Sprintf("worker owner manifest routeAllowlist[%d]", index)
	if route.Surface != "management" && route.Surface != "public" && route.Surface != "gateway" {
		return workerOwnerParsedRoute{}, fmt.Errorf("%s surface must be management, public, or gateway", label)
	}
	switch route.Method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
	default:
		return workerOwnerParsedRoute{}, fmt.Errorf("%s method must be an explicit uppercase HTTP method", label)
	}
	if (route.Owner != "node" && route.Owner != "go") || (route.RollbackOwner != "node" && route.RollbackOwner != "go") {
		return workerOwnerParsedRoute{}, fmt.Errorf("%s owner and rollbackOwner must be node or go", label)
	}
	if route.Owner == route.RollbackOwner {
		return workerOwnerParsedRoute{}, fmt.Errorf("%s owner and rollbackOwner must differ", label)
	}
	segments, err := parseWorkerOwnerCanonicalPath(route.Path, label+" path")
	if err != nil {
		return workerOwnerParsedRoute{}, err
	}
	path := route.Path
	if route.Surface == "management" && path != "/__aisys__/api" && !strings.HasPrefix(path, "/__aisys__/api/") {
		return workerOwnerParsedRoute{}, fmt.Errorf("%s path is outside the management surface", label)
	}
	if route.Surface == "public" && path != "/__aipublic__" && !strings.HasPrefix(path, "/__aipublic__/") {
		return workerOwnerParsedRoute{}, fmt.Errorf("%s path is outside the public surface", label)
	}
	if route.Surface == "gateway" && (path == "/__aisys__" || strings.HasPrefix(path, "/__aisys__/") || path == "/__aipublic__" || strings.HasPrefix(path, "/__aipublic__/")) {
		return workerOwnerParsedRoute{}, fmt.Errorf("%s path uses a reserved surface prefix", label)
	}
	if route.Surface == "gateway" && len(segments) > 0 && workerOwnerParameterSegmentPattern.MatchString(segments[0]) {
		return workerOwnerParsedRoute{}, fmt.Errorf("%s gateway first segment must be literal", label)
	}
	return workerOwnerParsedRoute{index: index, method: route.Method, segments: segments}, nil
}

func parseWorkerOwnerCanonicalPath(path, label string) ([]string, error) {
	if strings.TrimSpace(path) == "" || !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("%s must be a non-empty absolute path", label)
	}
	if strings.ContainsAny(path, "?#\\%*") {
		return nil, fmt.Errorf("%s must not contain a query, fragment, backslash, encoded byte, or wildcard", label)
	}
	if path != "/" && (strings.HasSuffix(path, "/") || strings.Contains(path, "//")) {
		return nil, fmt.Errorf("%s must not contain empty or trailing segments", label)
	}
	if path == "/" {
		return []string{}, nil
	}
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	parameterNames := make(map[string]struct{})
	for _, segment := range segments {
		if segment == "." || segment == ".." {
			return nil, fmt.Errorf("%s must not contain dot segments", label)
		}
		if strings.HasPrefix(segment, ":") {
			return nil, fmt.Errorf("%s must not contain unsafe wildcard segments", label)
		}
		parameterName, isParameter := workerOwnerParameterName(segment)
		if isParameter {
			if _, exists := parameterNames[parameterName]; exists {
				return nil, fmt.Errorf("%s must use unique parameter names", label)
			}
			parameterNames[parameterName] = struct{}{}
			continue
		}
		if strings.ContainsAny(segment, "{}") {
			return nil, fmt.Errorf("%s template parameters must occupy a complete segment", label)
		}
		if !workerOwnerLiteralSegmentPattern.MatchString(segment) {
			return nil, fmt.Errorf("%s contains unsupported path characters", label)
		}
	}
	return segments, nil
}

func workerOwnerParameterName(segment string) (string, bool) {
	if match := workerOwnerParameterSegmentPattern.FindStringSubmatch(segment); match != nil {
		return match[1], true
	}
	if match := workerOwnerActionParameterSegmentPattern.FindStringSubmatch(segment); match != nil {
		return match[1], true
	}
	return "", false
}

func workerOwnerPathTemplatesOverlap(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !workerOwnerSegmentTemplatesOverlap(left[index], right[index]) {
			return false
		}
	}
	return true
}

func workerOwnerSegmentTemplatesOverlap(left, right string) bool {
	if workerOwnerParameterSegmentPattern.MatchString(left) || workerOwnerParameterSegmentPattern.MatchString(right) {
		return true
	}
	leftSuffix, leftAction := workerOwnerActionSuffix(left)
	rightSuffix, rightAction := workerOwnerActionSuffix(right)
	if leftAction && rightAction {
		return leftSuffix == rightSuffix
	}
	if leftAction {
		return len(right) > len(leftSuffix) && strings.HasSuffix(right, leftSuffix)
	}
	if rightAction {
		return len(left) > len(rightSuffix) && strings.HasSuffix(left, rightSuffix)
	}
	return left == right
}

func workerOwnerActionSuffix(segment string) (string, bool) {
	match := workerOwnerActionParameterSegmentPattern.FindStringSubmatch(segment)
	if match == nil {
		return "", false
	}
	return match[2], true
}
