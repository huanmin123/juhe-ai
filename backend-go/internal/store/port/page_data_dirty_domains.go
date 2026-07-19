package port

import "context"

type PageDataDirtyDomain struct {
	Domain     string
	Generation int64
}

type PageDataDirtyDomainStore interface {
	ListPageDataDirtyDomains(ctx context.Context) ([]PageDataDirtyDomain, error)
	MarkPageDataDomainDirty(ctx context.Context, domain string) (int64, error)
	ClearPageDataDomainDirty(ctx context.Context, domain string, generation int64) (bool, error)
}
