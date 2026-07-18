package app

import (
	"context"
	"fmt"

	redisplatform "juhe-ai/backend-go/internal/platform/redis"
)

type pageDataCorePublisher interface {
	NewAccountsStaticResetEvents(ownerIDs []string, allScopes bool) ([]redisplatform.PageDataChangeEvent, error)
	Publish(ctx context.Context, event redisplatform.PageDataChangeEvent) error
}

type accountsStaticResetPublisherAdapter struct {
	publisher pageDataCorePublisher
}

func newAccountsStaticResetPublisher(
	client *redisplatform.Client,
	redisNamespace string,
) (accountsStaticResetPublisherAdapter, error) {
	publisher, err := redisplatform.NewPageDataChangePublisher(client, redisNamespace)
	if err != nil {
		return accountsStaticResetPublisherAdapter{}, fmt.Errorf("initialize page data change publisher: %w", err)
	}
	return accountsStaticResetPublisherAdapter{publisher: publisher}, nil
}

func (p accountsStaticResetPublisherAdapter) PublishAccountsStaticReset(
	ctx context.Context,
	ownerSystemAccountIDs []string,
	allScopes bool,
) error {
	if p.publisher == nil {
		return fmt.Errorf("page data change publisher is required")
	}
	events, err := p.publisher.NewAccountsStaticResetEvents(ownerSystemAccountIDs, allScopes)
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
