package circuitruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	IndexVersion    = "1"
	IndexOwnerMode  = "go-runtime-state-v1"
	DefaultCapacity = 100000
)

type OwnerGate struct{ Confirmed, SchemaReady, NodeWriterStopped bool }

func (g OwnerGate) Ready() bool { return g.Confirmed && g.SchemaReady && g.NodeWriterStopped }

type Config struct {
	URL, Namespace string
	Capacity       int
	Retention      time.Duration
}

type Store struct {
	client  *Client
	runtime *AccountCircuitRuntimeStore
	gate    OwnerGate
}

func New(cfg Config, gate OwnerGate) (*Store, error) {
	if strings.TrimSpace(cfg.URL) == "" || strings.TrimSpace(cfg.Namespace) == "" {
		return nil, errors.New("account circuit runtime Redis URL and namespace are required")
	}
	if cfg.Capacity == 0 {
		cfg.Capacity = DefaultCapacity
	}
	if cfg.Retention == 0 {
		cfg.Retention = 5 * time.Minute
	}
	client, err := NewClient(cfg.URL, cfg.Namespace)
	if err != nil {
		return nil, err
	}
	runtime, err := NewAccountCircuitRuntimeStore(client, cfg.Retention, cfg.Capacity)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return &Store{client: client, runtime: runtime, gate: gate}, nil
}

func (s *Store) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.client == nil {
		return errors.New("account circuit runtime Redis client is required")
	}
	return s.client.Ping(ctx)
}
func (s *Store) CheckReady(ctx context.Context) error {
	if s == nil || s.client == nil || s.runtime == nil || !s.gate.Ready() {
		return errors.New("account circuit runtime owner gate is not satisfied")
	}
	if err := s.client.Ping(ctx); err != nil {
		return err
	}
	meta, err := s.client.client.HMGet(ctx, s.runtime.keys.indexMeta, "version", "status", "ownerMode").Result()
	if err != nil {
		return err
	}
	if len(meta) != 3 || fmt.Sprint(meta[0]) != IndexVersion || fmt.Sprint(meta[1]) != "ready" || fmt.Sprint(meta[2]) != IndexOwnerMode {
		return errors.New("account circuit runtime index is not ready")
	}
	return nil
}

// Runtime exposes the complete Redis Lua state machine only after CheckReady
// has passed. Callers must remain inside the Gateway process.
func (s *Store) Runtime() (*AccountCircuitRuntimeStore, error) {
	if s == nil || s.runtime == nil {
		return nil, errors.New("account circuit runtime store is required")
	}
	if !s.gate.Ready() {
		return nil, errors.New("account circuit runtime owner gate is not satisfied")
	}
	return s.runtime, nil
}
