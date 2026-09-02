package gometricsstore

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/gometrics"
)

type Sampler struct {
	Collector *gometrics.Collector
	Store     *gometrics.Store
	Interval  time.Duration
	Retention time.Duration
	baseline  bool
}

const cleanupInterval = time.Hour

func NewSampler(collector *gometrics.Collector, store *gometrics.Store, interval time.Duration) (*Sampler, error) {
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

func (s *Sampler) TrendHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/__aisys__/api/stats/go-runtime-trend" {
			http.NotFound(w, r)
			return
		}
		service := r.URL.Query().Get("service")
		role := r.URL.Query().Get("role")
		if service == "" {
			service = s.Collector.Service()
		}
		if role == "" {
			role = s.Collector.Role()
		}
		to, err := ParseUnixOrRFC3339(r.URL.Query().Get("to"))
		if err != nil {
			http.Error(w, "invalid to", http.StatusBadRequest)
			return
		}
		from, err := ParseUnixOrRFC3339(r.URL.Query().Get("from"))
		if err != nil {
			http.Error(w, "invalid from", http.StatusBadRequest)
			return
		}
		trend, err := s.Store.QueryTrend(r.Context(), service, role, from, to)
		if err != nil {
			if errors.Is(err, gometrics.ErrTrendRangeTooLarge) {
				http.Error(w, "Go metrics trend range too large", http.StatusBadRequest)
				return
			}
			http.Error(w, "Go metrics trend unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"runtimeKind": "go", "service": service, "role": role, "items": trend})
	})
}
