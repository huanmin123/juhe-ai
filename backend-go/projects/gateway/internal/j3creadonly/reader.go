// Package j3creadonly exposes the narrow J3c read boundary for published
// J3b health facts. It deliberately contains no database handle and no
// mutation method; the J3b owner remains responsible for publishing facts.
package j3creadonly

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckowner"
)

// HealthSource is the only dependency accepted by the J3c reader. Keeping
// this interface local prevents a consumer from acquiring a J3b Store and
// accidentally reaching a writer method.
type HealthSource interface {
	ReadHealthFact(context.Context, string, string) (modelcheckowner.HealthFact, bool, error)
}

// PublishedHealth is the credential-free, read-only view J3c may consume.
// It excludes enforcement, retry and storage controls owned by J3b.
type PublishedHealth struct {
	AccountID, SystemAccountID, StatHour, RunID string
	ProviderCode, Model, Profile, Level         string
	ObservedAt                                  time.Time
	Score, Threshold                            int
	ErrorCode, ErrorMessage                     string
}

// Reader adapts the J3b read port into a J3c-specific value object.
type Reader struct {
	source HealthSource
}

func New(source HealthSource) (*Reader, error) {
	if source == nil {
		return nil, errors.New("J3c health source is required")
	}
	return &Reader{source: source}, nil
}

// Read returns one explicitly scoped published health fact. Missing or
// malformed facts fail closed so J3c cannot infer quality from a partial row.
func (r *Reader) Read(ctx context.Context, accountID, statHour string) (PublishedHealth, bool, error) {
	if r == nil || r.source == nil {
		return PublishedHealth{}, false, errors.New("J3c health reader is not initialized")
	}
	accountID = strings.TrimSpace(accountID)
	statHour = strings.TrimSpace(statHour)
	if accountID == "" || statHour == "" {
		return PublishedHealth{}, false, errors.New("J3c health read scope is incomplete")
	}
	fact, found, err := r.source.ReadHealthFact(ctx, accountID, statHour)
	if err != nil {
		return PublishedHealth{}, false, err
	}
	if !found {
		return PublishedHealth{}, false, nil
	}
	if err := validate(fact, accountID, statHour); err != nil {
		return PublishedHealth{}, false, err
	}
	return PublishedHealth{
		AccountID:       fact.AccountID,
		SystemAccountID: fact.SystemAccountID,
		StatHour:        fact.StatHour,
		RunID:           fact.RunID,
		ProviderCode:    fact.ProviderCode,
		Model:           fact.Model,
		Profile:         fact.Profile,
		Level:           fact.Level,
		ObservedAt:      fact.ObservedAt,
		Score:           fact.Score,
		Threshold:       fact.Threshold,
		ErrorCode:       fact.ErrorCode,
		ErrorMessage:    fact.ErrorMessage,
	}, true, nil
}

func validate(fact modelcheckowner.HealthFact, accountID, statHour string) error {
	if strings.TrimSpace(fact.AccountID) != accountID || strings.TrimSpace(fact.StatHour) != statHour {
		return errors.New("J3c health fact scope mismatch")
	}
	if strings.TrimSpace(fact.SystemAccountID) == "" || strings.TrimSpace(fact.RunID) == "" || strings.TrimSpace(fact.ProviderCode) == "" || strings.TrimSpace(fact.Model) == "" || strings.TrimSpace(fact.Profile) == "" || strings.TrimSpace(fact.Level) == "" || fact.ObservedAt.IsZero() || fact.Score < 0 || fact.Score > 100 || fact.Threshold < 40 || fact.Threshold > 100 {
		return errors.New("J3c published health fact is incomplete")
	}
	return nil
}
