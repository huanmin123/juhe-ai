package gometrics

import (
	"context"
	"errors"
	"time"
)

// Sampler persists one runtime snapshot per Interval into the durable Store
// (go_runtime_metrics_samples + the hourly/daily aggregate windows). Every Go
// process (gateway, jobs) owns an in-process sampler; there is no cross-process
// metrics HTTP surface anymore. The first write only establishes the CPU delta
// baseline, so the persisted sequence never contains a partial CPU interval.
type Sampler struct {
	Collector *Collector
	Store     *Store
	Interval  time.Duration
	Retention time.Duration
	baseline  bool
}

const cleanupInterval = time.Hour

func NewSampler(collector *Collector, store *Store, interval time.Duration) (*Sampler, error) {
	if collector == nil || store == nil {
		return nil, errors.New("Go metrics sampler requires collector and store")
	}
	if interval <= 0 {
		interval = defaultInterval
	}
	return &Sampler{Collector: collector, Store: store, Interval: interval, Retention: 30 * 24 * time.Hour}, nil
}

func (s *Sampler) Run(ctx context.Context) error {
	if s == nil {
		return errors.New("nil Go metrics sampler")
	}
	if err := s.write(ctx); err != nil {
		return err
	}
	if err := s.prune(ctx); err != nil {
		return err
	}
	nextCleanup := time.Now().UTC().Add(cleanupInterval)
	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.write(ctx); err != nil {
				return err
			}
			if !time.Now().UTC().Before(nextCleanup) {
				if err := s.prune(ctx); err != nil {
					return err
				}
				nextCleanup = time.Now().UTC().Add(cleanupInterval)
			}
		}
	}
}

func (s *Sampler) write(ctx context.Context) error {
	sample := s.Collector.Snapshot()
	if !s.baseline {
		// Establish CPU delta baseline without writing a partial sample. This
		// keeps the first persisted CPU interval and all scraper traffic out of
		// the sampler's single process-time sequence.
		s.baseline = true
		return nil
	}
	_, err := s.Store.InsertSnapshot(ctx, sample)
	return err
}

func (s *Sampler) prune(ctx context.Context) error {
	retention := s.Retention
	if retention <= 0 {
		retention = 30 * 24 * time.Hour
	}
	return s.Store.PruneBefore(ctx, time.Now().UTC().Add(-retention))
}
