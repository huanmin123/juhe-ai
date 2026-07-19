package postgres

import (
	"context"
	"fmt"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func (s *Store) ListPageDataDirtyDomains(ctx context.Context) ([]port.PageDataDirtyDomain, error) {
	rows, err := s.queries().ListPageDataDirtyDomains(ctx)
	if err != nil {
		return nil, fmt.Errorf("list page data dirty domains: %w", err)
	}
	result := make([]port.PageDataDirtyDomain, 0, len(rows))
	for _, row := range rows {
		result = append(result, port.PageDataDirtyDomain{Domain: row.Domain, Generation: row.Generation})
	}
	return result, nil
}

func (s *Store) MarkPageDataDomainDirty(ctx context.Context, domain string) (int64, error) {
	generation, err := s.queries().MarkPageDataDomainDirty(ctx, domain)
	if err != nil {
		return 0, fmt.Errorf("mark page data domain %q dirty: %w", domain, err)
	}
	if generation < 1 {
		return 0, fmt.Errorf("mark page data domain %q dirty: invalid generation %d", domain, generation)
	}
	return generation, nil
}

func (s *Store) ClearPageDataDomainDirty(ctx context.Context, domain string, generation int64) (bool, error) {
	rows, err := s.queries().ClearPageDataDomainDirty(ctx, postgresqueries.ClearPageDataDomainDirtyParams{
		Domain: domain, Generation: generation,
	})
	if err != nil {
		return false, fmt.Errorf("clear page data domain %q generation %d: %w", domain, generation, err)
	}
	return rows == 1, nil
}
