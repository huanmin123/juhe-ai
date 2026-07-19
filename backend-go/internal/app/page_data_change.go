package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"juhe-ai/backend-go/internal/modules/accountpagedata"
	"juhe-ai/backend-go/internal/modules/gatewaycache"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
)

type pageDataCorePublisher interface {
	NewAccountStaticUpsertEvent(input redisplatform.AccountChangeInput) (redisplatform.PageDataChangeEvent, error)
	NewAccountStaticDeleteEvent(input redisplatform.AccountChangeInput) (redisplatform.PageDataChangeEvent, error)
	NewAccountRuntimeUpsertEvent(input redisplatform.AccountChangeInput) (redisplatform.PageDataChangeEvent, error)
	NewAnnouncementPublicChangeEvent(announcementID string, operation string, fieldMask []string) (redisplatform.PageDataChangeEvent, error)
	NewRangeResetEvents(domain string, ownerIDs []string, allScopes bool) ([]redisplatform.PageDataChangeEvent, error)
	Publish(ctx context.Context, event redisplatform.PageDataChangeEvent) error
}

type pageDataCacheVersionWriter interface {
	SetPageDataVersion(ctx context.Context, key, value string, ttl time.Duration) error
}

type redisPageDataCacheVersionWriter struct{ client *redisplatform.Client }

func (w redisPageDataCacheVersionWriter) SetPageDataVersion(ctx context.Context, key, value string, ttl time.Duration) error {
	return w.client.SetRaw(ctx, key, []byte(value), ttl)
}

type managementPageDataPublisher interface {
	accountpagedata.Publisher
	PublishAccountsStaticReset(ctx context.Context, ownerSystemAccountIDs []string, allScopes bool) error
	PublishPageDataReset(ctx context.Context, domain string, ownerSystemAccountIDs []string, allScopes bool) error
	PublishAnnouncementPublicChange(ctx context.Context, announcementID string, operation string, fieldMask []string) error
}

type accountsStaticResetPublisherAdapter struct {
	publisher      pageDataCorePublisher
	cache          pageDataCacheVersionWriter
	redisNamespace string
	logger         *slog.Logger
}

func newAccountsStaticResetPublisher(
	client *redisplatform.Client,
	cacheClient *redisplatform.Client,
	redisNamespace string,
) (accountsStaticResetPublisherAdapter, error) {
	publisher, err := redisplatform.NewPageDataChangePublisher(client, redisNamespace)
	if err != nil {
		return accountsStaticResetPublisherAdapter{}, fmt.Errorf("initialize page data change publisher: %w", err)
	}
	adapter := accountsStaticResetPublisherAdapter{publisher: publisher, redisNamespace: redisNamespace, logger: slog.Default()}
	if cacheClient != nil {
		adapter.cache = redisPageDataCacheVersionWriter{client: cacheClient}
	}
	return adapter, nil
}

func newRecoveringAccountsStaticResetPublisher(
	ctx context.Context,
	client *redisplatform.Client,
	cacheClient *redisplatform.Client,
	redisNamespace string,
	dirtyStore port.PageDataDirtyDomainStore,
	logger *slog.Logger,
) (accountsStaticResetPublisherAdapter, func(), error) {
	adapter, err := newAccountsStaticResetPublisher(client, cacheClient, redisNamespace)
	if err != nil {
		return accountsStaticResetPublisherAdapter{}, nil, err
	}
	recovering := newRecoveringPageDataCorePublisher(adapter.publisher, dirtyStore, logger)
	recovering.Start(ctx)
	adapter.publisher = recovering
	adapter.logger = logger
	return adapter, recovering.Close, nil
}

func (p accountsStaticResetPublisherAdapter) PublishAccountsStaticReset(
	ctx context.Context,
	ownerSystemAccountIDs []string,
	allScopes bool,
) error {
	if p.publisher == nil {
		return fmt.Errorf("page data change publisher is required")
	}
	var publishErr error
	domains := []string{"accounts.static", "accounts.options"}
	results := make(chan error, len(domains))
	for _, domain := range domains {
		go func(domain string) {
			results <- p.PublishPageDataReset(ctx, domain, append([]string(nil), ownerSystemAccountIDs...), allScopes)
		}(domain)
	}
	for range domains {
		publishErr = errors.Join(publishErr, <-results)
	}
	return publishErr
}

func (p accountsStaticResetPublisherAdapter) PublishPageDataReset(
	ctx context.Context,
	domain string,
	ownerSystemAccountIDs []string,
	allScopes bool,
) error {
	if p.publisher == nil {
		return fmt.Errorf("page data change publisher is required")
	}
	p.invalidatePageDataCache(ctx, domain)
	events, err := p.publisher.NewRangeResetEvents(domain, ownerSystemAccountIDs, allScopes)
	if err != nil {
		return err
	}
	for _, event := range events {
		if err := p.publisher.Publish(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

func (p accountsStaticResetPublisherAdapter) PublishAnnouncementPublicChange(
	ctx context.Context,
	announcementID string,
	operation string,
	fieldMask []string,
) error {
	if p.publisher == nil {
		return fmt.Errorf("page data change publisher is required")
	}
	event, err := p.publisher.NewAnnouncementPublicChangeEvent(announcementID, operation, fieldMask)
	if err != nil {
		return err
	}
	return p.publisher.Publish(ctx, event)
}

func (p accountsStaticResetPublisherAdapter) PublishAccountStaticChange(
	ctx context.Context,
	input accountpagedata.ChangeInput,
) error {
	if p.publisher == nil {
		return fmt.Errorf("page data change publisher is required")
	}
	type publishTask struct {
		domain string
		run    func() error
	}
	tasks := []publishTask{{
		domain: "accounts.static",
		run: func() error {
			p.invalidatePageDataCache(ctx, "accounts.static")
			coreInput := redisplatform.AccountChangeInput{
				AccountID:             input.AccountID,
				OwnerSystemAccountIDs: append([]string(nil), input.OwnerSystemAccountIDs...),
				FieldMask:             append([]string(nil), input.FieldMask...),
				MembershipChanged:     input.MembershipChanged,
				OrderChanged:          input.OrderChanged,
				FilterChanged:         input.FilterChanged,
				PageChanged:           input.PageChanged,
				AllScopes:             input.AllScopes,
			}
			var event redisplatform.PageDataChangeEvent
			var err error
			switch input.Operation {
			case accountpagedata.OperationUpsert:
				event, err = p.publisher.NewAccountStaticUpsertEvent(coreInput)
			case accountpagedata.OperationDelete:
				event, err = p.publisher.NewAccountStaticDeleteEvent(coreInput)
			default:
				return fmt.Errorf("unsupported account static page data operation %q", input.Operation)
			}
			if err != nil {
				return err
			}
			return p.publisher.Publish(ctx, event)
		},
	}}
	for _, domain := range []string{"accounts.options", "stats.overview", "stats.accountUsage", "stats.aiPerformance"} {
		domain := domain
		tasks = append(tasks, publishTask{
			domain: domain,
			run: func() error {
				return p.PublishPageDataReset(ctx, domain, append([]string(nil), input.OwnerSystemAccountIDs...), input.AllScopes)
			},
		})
	}
	results := make(chan error, len(tasks))
	for _, task := range tasks {
		go func(task publishTask) {
			results <- task.run()
		}(task)
	}
	var publishErr error
	for range tasks {
		publishErr = errors.Join(publishErr, <-results)
	}
	return publishErr
}

func (p accountsStaticResetPublisherAdapter) PublishAccountRuntimeChange(
	ctx context.Context,
	input accountpagedata.ChangeInput,
) error {
	if p.publisher == nil {
		return fmt.Errorf("page data change publisher is required")
	}
	if input.Operation != accountpagedata.OperationUpsert {
		return fmt.Errorf("unsupported account runtime page data operation %q", input.Operation)
	}
	results := make(chan error, 2)
	go func() {
		p.invalidatePageDataCache(ctx, "accounts.runtime")
		event, err := p.publisher.NewAccountRuntimeUpsertEvent(redisplatform.AccountChangeInput{
			AccountID:             input.AccountID,
			OwnerSystemAccountIDs: append([]string(nil), input.OwnerSystemAccountIDs...),
			FieldMask:             append([]string(nil), input.FieldMask...),
			MembershipChanged:     input.MembershipChanged,
			OrderChanged:          input.OrderChanged,
			FilterChanged:         input.FilterChanged,
			PageChanged:           input.PageChanged,
			AllScopes:             input.AllScopes,
		})
		if err == nil {
			err = p.publisher.Publish(ctx, event)
		}
		results <- err
	}()
	go func() {
		results <- p.PublishPageDataReset(ctx, "accounts.options", append([]string(nil), input.OwnerSystemAccountIDs...), input.AllScopes)
	}()
	return errors.Join(<-results, <-results)
}

func (p accountsStaticResetPublisherAdapter) invalidatePageDataCache(ctx context.Context, domain string) {
	logger := p.logger
	if logger == nil {
		logger = slog.Default()
	}
	if p.cache != nil {
		key, err := gatewaycache.SharedCacheVersionKey(p.redisNamespace, "page_data_"+strings.ReplaceAll(domain, ".", "_"))
		if err != nil {
			logger.Warn("页面数据后端缓存版本键生成失败", slog.String("domain", domain), slog.Any("error", err))
		} else if err := p.cache.SetPageDataVersion(ctx, key, uuid.NewString(), 30*24*time.Hour); err != nil {
			logger.Warn("页面数据后端缓存失效失败", slog.String("domain", domain), slog.String("key", key), slog.Any("error", err))
		}
	}
}
