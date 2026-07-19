package app

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	pageDataDirtyMarkTimeout  = 5 * time.Second
	pageDataRecoveryMaxDelay  = 30 * time.Second
	pageDataDirtyScanInterval = 30 * time.Second
)

type pageDataDirtyDomain = port.PageDataDirtyDomain

type pendingPageDataDirtyDomain struct {
	Domain     string
	Generation int64
	Revision   uint64
}

type recoveringPageDataCorePublisher struct {
	delegate pageDataCorePublisher
	store    port.PageDataDirtyDomainStore
	logger   *slog.Logger

	mu            sync.Mutex
	pending       map[string]pendingPageDataDirtyDomain
	nextRevision  uint64
	wake          chan struct{}
	cancel        context.CancelFunc
	done          chan struct{}
	started       bool
	scanInterval  time.Duration
	recoveryDelay func(int) time.Duration
}

func newRecoveringPageDataCorePublisher(delegate pageDataCorePublisher, store port.PageDataDirtyDomainStore, logger *slog.Logger) *recoveringPageDataCorePublisher {
	if logger == nil {
		logger = slog.Default()
	}
	return &recoveringPageDataCorePublisher{
		delegate:      delegate,
		store:         store,
		logger:        logger,
		pending:       make(map[string]pendingPageDataDirtyDomain),
		wake:          make(chan struct{}, 1),
		scanInterval:  pageDataDirtyScanInterval,
		recoveryDelay: pageDataRecoveryDelay,
	}
}

func (p *recoveringPageDataCorePublisher) Start(parent context.Context) {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel
	p.done = make(chan struct{})
	p.started = true
	done := p.done
	p.mu.Unlock()
	go func() {
		defer close(done)
		p.run(ctx)
	}()
}

func (p *recoveringPageDataCorePublisher) Close() {
	p.mu.Lock()
	cancel := p.cancel
	done := p.done
	p.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

func (p *recoveringPageDataCorePublisher) NewAccountStaticUpsertEvent(input redisplatform.AccountChangeInput) (redisplatform.PageDataChangeEvent, error) {
	return p.delegate.NewAccountStaticUpsertEvent(input)
}

func (p *recoveringPageDataCorePublisher) NewAccountStaticDeleteEvent(input redisplatform.AccountChangeInput) (redisplatform.PageDataChangeEvent, error) {
	return p.delegate.NewAccountStaticDeleteEvent(input)
}

func (p *recoveringPageDataCorePublisher) NewAccountRuntimeUpsertEvent(input redisplatform.AccountChangeInput) (redisplatform.PageDataChangeEvent, error) {
	return p.delegate.NewAccountRuntimeUpsertEvent(input)
}

func (p *recoveringPageDataCorePublisher) NewRangeResetEvents(domain string, ownerIDs []string, allScopes bool) ([]redisplatform.PageDataChangeEvent, error) {
	return p.delegate.NewRangeResetEvents(domain, ownerIDs, allScopes)
}

func (p *recoveringPageDataCorePublisher) Publish(ctx context.Context, event redisplatform.PageDataChangeEvent) error {
	publishErr := p.delegate.Publish(ctx, event)
	if publishErr == nil {
		return nil
	}
	markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pageDataDirtyMarkTimeout)
	generation, markErr := p.store.MarkPageDataDomainDirty(markCtx, event.Domain)
	cancel()
	if markErr != nil {
		generation = 0
		p.logger.Warn("页面数据 dirty domain 持久化失败", slog.String("domain", event.Domain), slog.Any("error", markErr))
	}
	p.recordPending(event.Domain, generation)
	p.signal()
	return errors.Join(publishErr, markErr)
}

func (p *recoveringPageDataCorePublisher) run(ctx context.Context) {
	loadPending := true
	retryRound := 0
	for {
		failed := false
		if loadPending {
			if err := p.loadPersistent(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				failed = true
				p.logger.Warn("页面数据 dirty domain 启动加载失败，稍后重试", slog.Any("error", err))
			} else {
				loadPending = false
			}
		}
		if !failed && p.hasPending() {
			failed = p.recoverOnce(ctx)
		}
		if ctx.Err() != nil {
			return
		}
		if !failed && !p.hasPending() {
			retryRound = 0
			timer := time.NewTimer(p.scanInterval)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-p.wake:
				if !timer.Stop() {
					<-timer.C
				}
				loadPending = true
			case <-timer.C:
				loadPending = true
			}
			continue
		}
		delay := p.recoveryDelay(retryRound)
		retryRound++
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
			loadPending = true
		}
	}
}

func (p *recoveringPageDataCorePublisher) loadPersistent(ctx context.Context) error {
	rows, err := p.store.ListPageDataDirtyDomains(ctx)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, row := range rows {
		current := p.pending[row.Domain]
		if row.Domain != "" && row.Generation > current.Generation {
			p.nextRevision++
			current.Domain = row.Domain
			current.Generation = row.Generation
			current.Revision = p.nextRevision
			p.pending[row.Domain] = current
		}
	}
	return nil
}

func (p *recoveringPageDataCorePublisher) recoverOnce(ctx context.Context) bool {
	pending := p.pendingSnapshot()
	failed := false
	for _, row := range pending {
		events, err := p.delegate.NewRangeResetEvents(row.Domain, nil, true)
		if err == nil {
			for _, event := range events {
				if err = p.delegate.Publish(ctx, event); err != nil {
					break
				}
			}
		}
		if err != nil {
			failed = true
			p.logger.Warn("页面数据 dirty domain 恢复失败", slog.String("domain", row.Domain), slog.Int64("generation", row.Generation), slog.Any("error", err))
			continue
		}
		if row.Generation == 0 {
			p.deletePendingIfRevision(row.Domain, row.Revision)
			continue
		}
		cleared, clearErr := p.store.ClearPageDataDomainDirty(ctx, row.Domain, row.Generation)
		if clearErr != nil {
			failed = true
			p.logger.Warn("页面数据 dirty domain 清理失败", slog.String("domain", row.Domain), slog.Int64("generation", row.Generation), slog.Any("error", clearErr))
			continue
		}
		if cleared {
			p.deletePendingIfRevision(row.Domain, row.Revision)
			continue
		}
		if err := p.refreshPersistentDomain(ctx, row); err != nil {
			p.logger.Warn("页面数据 dirty domain 代际刷新失败", slog.String("domain", row.Domain), slog.Any("error", err))
		}
		failed = true
	}
	return failed
}

func (p *recoveringPageDataCorePublisher) refreshPersistentDomain(ctx context.Context, expected pendingPageDataDirtyDomain) error {
	rows, err := p.store.ListPageDataDirtyDomains(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Domain == expected.Domain {
			p.mergePersistent(row)
			return nil
		}
	}
	p.deletePendingIfRevision(expected.Domain, expected.Revision)
	return nil
}

func (p *recoveringPageDataCorePublisher) recordPending(domain string, generation int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	current := p.pending[domain]
	p.nextRevision++
	current.Domain = domain
	current.Revision = p.nextRevision
	if generation > current.Generation {
		current.Generation = generation
	}
	p.pending[domain] = current
}

func (p *recoveringPageDataCorePublisher) mergePersistent(row pageDataDirtyDomain) {
	p.mu.Lock()
	defer p.mu.Unlock()
	current := p.pending[row.Domain]
	if row.Generation <= current.Generation {
		return
	}
	p.nextRevision++
	current.Domain = row.Domain
	current.Generation = row.Generation
	current.Revision = p.nextRevision
	p.pending[row.Domain] = current
}

func (p *recoveringPageDataCorePublisher) deletePendingIfRevision(domain string, revision uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending[domain].Revision == revision {
		delete(p.pending, domain)
	}
}

func (p *recoveringPageDataCorePublisher) pendingSnapshot() []pendingPageDataDirtyDomain {
	p.mu.Lock()
	defer p.mu.Unlock()
	rows := make([]pendingPageDataDirtyDomain, 0, len(p.pending))
	for _, row := range p.pending {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Domain < rows[j].Domain })
	return rows
}

func (p *recoveringPageDataCorePublisher) hasPending() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pending) > 0
}

func (p *recoveringPageDataCorePublisher) pendingGeneration(domain string) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pending[domain].Generation
}

func (p *recoveringPageDataCorePublisher) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func pageDataRecoveryDelay(round int) time.Duration {
	if round < 0 {
		round = 0
	}
	shift := min(round, 5)
	delay := time.Second * time.Duration(1<<shift)
	return min(delay, pageDataRecoveryMaxDelay)
}
