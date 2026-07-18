package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"juhe-ai/backend-go/internal/modules/gatewaycache"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
)

type pageDataCorePublisher interface {
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
	PublishAccountsStaticReset(ctx context.Context, ownerSystemAccountIDs []string, allScopes bool) error
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

func (p accountsStaticResetPublisherAdapter) PublishAccountsStaticReset(
	ctx context.Context,
	ownerSystemAccountIDs []string,
	allScopes bool,
) error {
	if p.publisher == nil {
		return fmt.Errorf("page data change publisher is required")
	}
	logger := p.logger
	if logger == nil {
		logger = slog.Default()
	}
	for _, domain := range []string{"accounts.static", "accounts.options"} {
		if p.cache != nil {
			key, err := gatewaycache.SharedCacheVersionKey(p.redisNamespace, "page_data_"+strings.ReplaceAll(domain, ".", "_"))
			if err != nil {
				logger.Warn("页面数据后端缓存版本键生成失败", slog.String("domain", domain), slog.Any("error", err))
			} else if err := p.cache.SetPageDataVersion(ctx, key, uuid.NewString(), 30*24*time.Hour); err != nil {
				logger.Warn("页面数据后端缓存失效失败", slog.String("domain", domain), slog.String("key", key), slog.Any("error", err))
			}
		}
		events, err := p.publisher.NewRangeResetEvents(domain, ownerSystemAccountIDs, allScopes)
		if err != nil {
			return err
		}
		for _, event := range events {
			if err := p.publisher.Publish(ctx, event); err != nil {
				return err
			}
		}
	}
	return nil
}
