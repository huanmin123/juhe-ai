package accountpagedata

import (
	"context"
	"errors"
	"sort"
	"strings"
)

type Operation string

const (
	OperationUpsert Operation = "upsert"
	OperationDelete Operation = "delete"
	MaxEventOwners            = 256
)

type ChangeInput struct {
	AccountID             string
	Operation             Operation
	OwnerSystemAccountIDs []string
	FieldMask             []string
	MembershipChanged     bool
	OrderChanged          bool
	FilterChanged         bool
	PageChanged           bool
	AllScopes             bool
}

type Publisher interface {
	PublishAccountStaticChange(ctx context.Context, input ChangeInput) error
	PublishAccountRuntimeChange(ctx context.Context, input ChangeInput) error
}

type GranteeReader interface {
	ListAccountAuthorizationGranteeIDs(ctx context.Context, accountID string) ([]string, error)
}

var ErrGranteeReaderRequired = errors.New("account page data grantee reader is required")

func ResolveOwners(ctx context.Context, reader GranteeReader, accountID string, knownOwners []string) ([]string, bool, error) {
	owners := NormalizeOwnerIDs(knownOwners)
	if reader == nil {
		return owners, true, ErrGranteeReaderRequired
	}
	granteeIDs, err := reader.ListAccountAuthorizationGranteeIDs(ctx, strings.TrimSpace(accountID))
	if err != nil {
		return owners, true, err
	}
	owners = NormalizeOwnerIDs(append(owners, granteeIDs...))
	if len(owners) > MaxEventOwners {
		return []string{}, true, nil
	}
	return owners, false, nil
}

func NormalizeOwnerIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
