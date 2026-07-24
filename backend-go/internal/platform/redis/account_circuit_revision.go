package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	goredis "github.com/redis/go-redis/v9"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	AccountCircuitRedisStoreName         = "gateway-account-circuit"
	DefaultAccountCircuitClosedRetention = 5 * time.Minute
)

var invalidAccountCircuitNamespaceChars = regexp.MustCompile(`[^a-zA-Z0-9_.:-]+`)

const projectAccountCircuitRevisionLua = accountCircuitRuntimeReplaceAccountRevisionLua

var projectAccountCircuitRevisionScript = goredis.NewScript(projectAccountCircuitRevisionLua)

type accountCircuitRevisionKeys struct {
	states          string
	due             string
	closed          string
	escalation      string
	revisions       string
	scopeRuntime    string
	runtimeScopes   string
	accountRuntimes string
	runtimeAccounts string
	ledgerRevisions string
	indexMeta       string
	indexLock       string
}

type AccountCircuitRevisionProjector struct {
	keys            accountCircuitRevisionKeys
	retention       time.Duration
	maxIndexMembers int
	now             func() time.Time
	project         func(context.Context, accountCircuitRevisionKeys, port.GatewayAccountCircuitOutboxEvent, time.Duration, time.Time) ([]byte, error)
}

func NewAccountCircuitRevisionProjector(client *Client, retention time.Duration) (*AccountCircuitRevisionProjector, error) {
	if client == nil || client.client == nil {
		return nil, fmt.Errorf("Redis state client is required")
	}
	if retention == 0 {
		retention = DefaultAccountCircuitClosedRetention
	}
	if retention <= 0 || retention > 24*time.Hour {
		return nil, fmt.Errorf("account circuit closed retention is invalid")
	}
	keys, err := accountCircuitRevisionRedisKeys(client.namespace, AccountCircuitRedisStoreName)
	if err != nil {
		return nil, err
	}
	projector := &AccountCircuitRevisionProjector{keys: keys, retention: retention, maxIndexMembers: DefaultAccountCircuitRuntimeIndexMaxScopeMembers, now: time.Now}
	projector.project = func(ctx context.Context, keys accountCircuitRevisionKeys, event port.GatewayAccountCircuitOutboxEvent, retention time.Duration, now time.Time) ([]byte, error) {
		value, err := projectAccountCircuitRevisionScript.Run(
			ctx,
			client.client,
			[]string{keys.states, keys.due, keys.closed, keys.escalation, keys.revisions, keys.scopeRuntime, keys.runtimeScopes, keys.accountRuntimes, keys.runtimeAccounts, keys.indexMeta},
			event.AccountRuntimeKey,
			strconv.FormatInt(event.DispatchRevision, 10),
			event.TransitionID,
			strconv.FormatInt(now.UTC().UnixMilli(), 10),
			strconv.FormatInt(retention.Milliseconds(), 10),
			strconv.Itoa(projector.maxIndexMembers),
		).Result()
		if err != nil {
			return nil, err
		}
		encoded, err := runtimeRedisBytes(value)
		if err != nil {
			return nil, err
		}
		var response accountCircuitRuntimeAccountRevisionResponseWire
		if err := json.Unmarshal(encoded, &response); err != nil {
			return nil, fmt.Errorf("decode indexed account circuit revision projection: %w", err)
		}
		var status port.GatewayAccountCircuitRevisionProjectionStatus
		switch response.Status {
		case string(port.GatewayAccountCircuitMutationApplied):
			status = port.GatewayAccountCircuitRevisionApplied
		case string(port.GatewayAccountCircuitMutationIdempotent):
			status = port.GatewayAccountCircuitRevisionIdempotent
		case string(port.GatewayAccountCircuitMutationStaleDispatchRevision):
			status = port.GatewayAccountCircuitRevisionStale
		default:
			return nil, fmt.Errorf("indexed account circuit revision status is invalid")
		}
		if response.CurrentDispatchRevision < 1 || response.ClosedScopeCount < 0 {
			return nil, fmt.Errorf("indexed account circuit revision result is invalid")
		}
		return json.Marshal(port.GatewayAccountCircuitRevisionProjection{Status: status, CurrentRevision: response.CurrentDispatchRevision, ClosedStates: response.ClosedScopeCount})
	}
	return projector, nil
}

func (p *AccountCircuitRevisionProjector) WithNow(now func() time.Time) *AccountCircuitRevisionProjector {
	if now != nil {
		p.now = now
	}
	return p
}

func (p *AccountCircuitRevisionProjector) WithMaxIndexMembers(maxIndexMembers int) *AccountCircuitRevisionProjector {
	if maxIndexMembers > 0 && maxIndexMembers <= 1000000 {
		p.maxIndexMembers = maxIndexMembers
	}
	return p
}

func (p *AccountCircuitRevisionProjector) ProjectGatewayAccountCircuitRevision(ctx context.Context, event port.GatewayAccountCircuitOutboxEvent) (port.GatewayAccountCircuitRevisionProjection, error) {
	if p == nil || p.project == nil {
		return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("account circuit revision projector is required")
	}
	if ctx == nil {
		return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("account circuit revision context is required")
	}
	if err := validateAccountCircuitRevisionEvent(event); err != nil {
		return port.GatewayAccountCircuitRevisionProjection{}, err
	}
	raw, err := p.project(ctx, p.keys, event, p.retention, p.now())
	if err != nil {
		return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("project account circuit dispatch revision: %w", err)
	}
	var result port.GatewayAccountCircuitRevisionProjection
	if err := json.Unmarshal(raw, &result); err != nil {
		return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("decode account circuit revision projection: %w", err)
	}
	if err := validateAccountCircuitRevisionProjection(result); err != nil {
		return port.GatewayAccountCircuitRevisionProjection{}, err
	}
	return result, nil
}

func accountCircuitRevisionRedisKeys(namespace, name string) (accountCircuitRevisionKeys, error) {
	namespace = invalidAccountCircuitNamespaceChars.ReplaceAllString(strings.TrimSpace(namespace), "_")
	namespace = strings.Trim(namespace, "_")
	name = invalidAccountCircuitNamespaceChars.ReplaceAllString(strings.TrimSpace(name), "_")
	name = strings.Trim(name, "_")
	if namespace == "" || name == "" {
		return accountCircuitRevisionKeys{}, fmt.Errorf("account circuit Redis namespace is invalid")
	}
	prefix := "juhe-ai:" + namespace + ":account-circuit:" + name
	return accountCircuitRevisionKeys{
		states: prefix + ":states", due: prefix + ":due", closed: prefix + ":closed",
		escalation: prefix + ":escalation", revisions: prefix + ":dispatch-revisions",
		scopeRuntime: prefix + ":scope-runtime", runtimeScopes: prefix + ":runtime-scopes",
		accountRuntimes: prefix + ":account-runtimes", indexMeta: prefix + ":runtime-index-meta",
		runtimeAccounts: prefix + ":runtime-accounts",
		ledgerRevisions: prefix + ":ledger-revisions",
		indexLock:       prefix + ":runtime-index-lock",
	}, nil
}

func validateAccountCircuitRevisionEvent(event port.GatewayAccountCircuitOutboxEvent) error {
	if event.EventType != port.GatewayAccountCircuitDispatchRevisionChanged || event.ProjectionKey != port.GatewayAccountCircuitProjectionKey || event.AccountRuntimeKey != event.AccountID || !validAccountCircuitRevisionText(event.AccountID, 1024) || !validAccountCircuitRevisionText(event.TransitionID, 256) || event.DispatchRevision < 1 {
		return fmt.Errorf("account circuit revision event is invalid")
	}
	return nil
}

func validateAccountCircuitRevisionProjection(value port.GatewayAccountCircuitRevisionProjection) error {
	if value.CurrentRevision < 1 || value.ClosedStates < 0 {
		return fmt.Errorf("account circuit revision projection result is invalid")
	}
	switch value.Status {
	case port.GatewayAccountCircuitRevisionApplied, port.GatewayAccountCircuitRevisionIdempotent, port.GatewayAccountCircuitRevisionStale:
		return nil
	default:
		return fmt.Errorf("account circuit revision projection status is invalid")
	}
}

func validAccountCircuitRevisionText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

var _ port.GatewayAccountCircuitRevisionProjector = (*AccountCircuitRevisionProjector)(nil)
