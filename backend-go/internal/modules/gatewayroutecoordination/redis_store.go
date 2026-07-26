package gatewayroutecoordination

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	redisplatform "juhe-ai/backend-go/internal/platform/redis"
)

const (
	RedisStateTTL           = 30 * 24 * time.Hour
	RedisStateMaxCASRetries = 64
	redisStateFormatVersion = "v1"
	redisStateWireVersion   = 1
	redisStateMaxBytes      = 4 * 1024
)

// RedisStore shares only route-order state across Go instances. It uses a
// bounded CAS loop around Advance, keeping the decision algorithm in Go and
// the Redis primitive generic. It deliberately uses a Go-only keyspace and is
// not registered by an HTTP listener or application constructor.
type RedisStore struct {
	get            func(context.Context, string) ([]byte, error)
	compareAndSwap func(context.Context, string, []byte, []byte, time.Duration) (bool, error)
	ttl            time.Duration
	maxCASRetries  int
}

func NewRedisStore(client *redisplatform.Client) (*RedisStore, error) {
	if client == nil {
		return nil, fmt.Errorf("redis route coordination client is required")
	}
	return &RedisStore{
		get:            client.Get,
		compareAndSwap: client.CompareAndSwap,
		ttl:            RedisStateTTL,
		maxCASRetries:  RedisStateMaxCASRetries,
	}, nil
}

func (s *RedisStore) Plan(ctx context.Context, snapshot Snapshot) (Plan, error) {
	if ctx == nil {
		return Plan{}, fmt.Errorf("route coordination context is required")
	}
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	if s == nil || s.get == nil || s.compareAndSwap == nil {
		return Plan{}, fmt.Errorf("redis route coordination store is not initialized")
	}
	if s.ttl <= 0 || s.maxCASRetries <= 0 {
		return Plan{}, fmt.Errorf("redis route coordination store has invalid limits")
	}
	if snapshot.DispatchGeneration < 1 {
		return Plan{}, fmt.Errorf("redis route coordination requires a persistent dispatch generation")
	}
	key, err := redisStateKey(snapshot.Scope)
	if err != nil {
		return Plan{}, err
	}

	baseline, _, err := Advance(snapshot, SharedState{})
	if err != nil {
		return Plan{}, err
	}
	if !baseline.StateAdvanced {
		return s.deleteStaleState(ctx, key, snapshot, baseline)
	}

	for attempt := 0; attempt < s.maxCASRetries; attempt++ {
		raw, err := s.get(ctx, key)
		exists := err == nil
		if err != nil && !errors.Is(err, redisplatform.ErrNotFound) {
			return Plan{}, fmt.Errorf("load redis route coordination state: %w", err)
		}
		current := SharedState{}
		if exists {
			current, err = decodeRedisState(raw)
			if err != nil {
				return Plan{}, err
			}
		}
		plan, next, err := Advance(snapshot, current)
		if err != nil {
			return Plan{}, err
		}
		rawNext, err := encodeRedisState(next)
		if err != nil {
			return Plan{}, err
		}
		expected := raw
		if !exists {
			expected = nil
		}
		swapped, err := s.compareAndSwap(ctx, key, expected, rawNext, s.ttl)
		if err != nil {
			return Plan{}, fmt.Errorf("save redis route coordination state: %w", err)
		}
		if swapped {
			return plan, nil
		}
		if err := waitForRedisCASRetry(ctx, attempt); err != nil {
			return Plan{}, err
		}
	}
	return Plan{}, fmt.Errorf("redis route coordination state changed too often")
}

func (s *RedisStore) deleteStaleState(ctx context.Context, key string, snapshot Snapshot, plan Plan) (Plan, error) {
	for attempt := 0; attempt < s.maxCASRetries; attempt++ {
		raw, err := s.get(ctx, key)
		if errors.Is(err, redisplatform.ErrNotFound) {
			return plan, nil
		}
		if err != nil {
			return Plan{}, fmt.Errorf("load stale redis route coordination state: %w", err)
		}
		current, err := decodeRedisState(raw)
		if err != nil {
			return Plan{}, err
		}
		if _, _, err := Advance(snapshot, current); err != nil {
			return Plan{}, err
		}
		swapped, err := s.compareAndSwap(ctx, key, raw, nil, s.ttl)
		if err != nil {
			return Plan{}, fmt.Errorf("delete stale redis route coordination state: %w", err)
		}
		if swapped {
			return plan, nil
		}
		if err := waitForRedisCASRetry(ctx, attempt); err != nil {
			return Plan{}, err
		}
	}
	return Plan{}, fmt.Errorf("stale redis route coordination state changed too often")
}

func waitForRedisCASRetry(ctx context.Context, attempt int) error {
	shift := attempt
	if shift > 5 {
		shift = 5
	}
	delay := 100 * time.Microsecond * time.Duration(1<<shift)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func redisStateKey(scope Scope) (string, error) {
	if !safeIdentity(scope.SystemAccountID) || !safeIdentity(scope.RouteStrategyID) {
		return "", fmt.Errorf("route coordination scope is required")
	}
	return "gateway-route-coordination:" + redisStateFormatVersion + ":" +
		base64.RawURLEncoding.EncodeToString([]byte(scope.SystemAccountID)) + ":" +
		base64.RawURLEncoding.EncodeToString([]byte(scope.RouteStrategyID)), nil
}

func decodeRedisState(raw []byte) (SharedState, error) {
	if len(raw) == 0 || len(raw) > redisStateMaxBytes {
		return SharedState{}, fmt.Errorf("redis route coordination state has invalid size")
	}
	var record redisStateRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return SharedState{}, fmt.Errorf("decode redis route coordination state: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return SharedState{}, fmt.Errorf("redis route coordination state has trailing data")
	}
	state := SharedState{DispatchGeneration: record.DispatchGeneration, Revision: record.Revision, Sequence: record.Sequence, Weighted: record.Weighted}
	if record.Version != redisStateWireVersion || state.DispatchGeneration < 1 || len(state.Revision) == 0 || len(state.Revision) > 128 || state.Sequence < 0 || state.Sequence == maxRouteSequence || len(state.Weighted) > MaxBindings {
		return SharedState{}, fmt.Errorf("redis route coordination state exceeds limits")
	}
	for key, value := range state.Weighted {
		if !safeIdentity(key) || value < -MaxBindings*100 || value > MaxBindings*100 {
			return SharedState{}, fmt.Errorf("redis route coordination state has invalid weighted entry")
		}
	}
	return cloneSharedState(state), nil
}

func encodeRedisState(state SharedState) ([]byte, error) {
	if state.DispatchGeneration < 1 || len(state.Revision) == 0 || len(state.Revision) > 128 || state.Sequence < 0 || state.Sequence == maxRouteSequence || len(state.Weighted) > MaxBindings {
		return nil, fmt.Errorf("redis route coordination state exceeds limits")
	}
	for key, value := range state.Weighted {
		if !safeIdentity(key) || value < -MaxBindings*100 || value > MaxBindings*100 {
			return nil, fmt.Errorf("redis route coordination state has invalid weighted entry")
		}
	}
	raw, err := json.Marshal(redisStateRecord{Version: redisStateWireVersion, DispatchGeneration: state.DispatchGeneration, Revision: state.Revision, Sequence: state.Sequence, Weighted: cloneState(state.Weighted)})
	if err != nil {
		return nil, fmt.Errorf("encode redis route coordination state: %w", err)
	}
	if len(raw) > redisStateMaxBytes {
		return nil, fmt.Errorf("encoded redis route coordination state exceeds limits")
	}
	return raw, nil
}

type redisStateRecord struct {
	Version            int            `json:"v"`
	DispatchGeneration int64          `json:"g"`
	Revision           string         `json:"r"`
	Sequence           int64          `json:"s"`
	Weighted           map[string]int `json:"w,omitempty"`
}
