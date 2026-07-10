package gatewayquotasnapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycache"
)

const (
	RuntimeStateStoreName          = "gateway_quota_snapshot"
	RuntimeStateCurrentKey         = "current"
	DefaultRuntimeStateSnapshotTTL = 5 * time.Minute
)

type RuntimeStatePublisher struct {
	state     gatewaycache.RawSetter
	namespace string
	ttl       time.Duration
}

type RuntimeStatePublisherOptions struct {
	State     gatewaycache.RawSetter
	Namespace string
	TTL       time.Duration
}

func NewRuntimeStatePublisher(opts RuntimeStatePublisherOptions) (*RuntimeStatePublisher, error) {
	if opts.State == nil {
		return nil, fmt.Errorf("gateway quota snapshot runtime state redis setter is required")
	}
	namespace := strings.TrimSpace(opts.Namespace)
	if namespace == "" {
		return nil, fmt.Errorf("redis namespace is required")
	}
	if _, err := gatewaycache.SanitizeNamespacePart(namespace); err != nil {
		return nil, err
	}
	ttl := opts.TTL
	if ttl == 0 {
		ttl = DefaultRuntimeStateSnapshotTTL
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("gateway quota snapshot runtime state ttl must be greater than 0")
	}
	return &RuntimeStatePublisher{
		state:     opts.State,
		namespace: namespace,
		ttl:       ttl,
	}, nil
}

func (p *RuntimeStatePublisher) Publish(ctx context.Context, snapshot Snapshot) error {
	if p == nil {
		return fmt.Errorf("gateway quota snapshot runtime state publisher is required")
	}
	key, err := gatewaycache.RuntimeStateKey(p.namespace, RuntimeStateStoreName, RuntimeStateCurrentKey)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal gateway quota snapshot runtime state: %w", err)
	}
	if err := p.state.SetRaw(ctx, key, payload, p.ttl); err != nil {
		return fmt.Errorf("publish gateway quota snapshot runtime state: %w", err)
	}
	return nil
}
