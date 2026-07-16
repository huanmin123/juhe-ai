package managementroutestrategies

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultOptionLimit = 50
	maxOptionLimit     = 100
)

type Service struct {
	store        port.ManagementRouteStrategyOptionReader
	listReader   port.ManagementRouteStrategyListReader
	detailReader port.ManagementRouteStrategyDetailReader
	createStore  port.PublicRouteStrategyStore
	transactor   port.PublicRouteStrategyTransactor
	invalidator  RuntimeInvalidator
	logger       *slog.Logger
	now          func() time.Time
	newID        func(prefix string) string
}

type ServiceOptions struct {
	OptionReader port.ManagementRouteStrategyOptionReader
	ListReader   port.ManagementRouteStrategyListReader
	DetailReader port.ManagementRouteStrategyDetailReader
	CreateStore  port.PublicRouteStrategyStore
	Transactor   port.PublicRouteStrategyTransactor
	Invalidator  RuntimeInvalidator
	Logger       *slog.Logger
	Now          func() time.Time
	NewID        func(prefix string) string
}

type OptionListInput struct {
	SystemAccountID            string
	IncludeSystemAccountFields bool
	IDs                        []string
	Keyword                    string
	Limit                      int
	ActiveOnly                 bool
}

type Option struct {
	ID                string `json:"id"`
	SystemAccountID   string `json:"systemAccountId,omitempty"`
	SystemAccountName string `json:"systemAccountName,omitempty"`
	Name              string `json:"name"`
	Mode              string `json:"mode"`
	Status            string `json:"status"`
	IsDefault         bool   `json:"isDefault"`
}

func NewService(store port.ManagementRouteStrategyOptionReader) *Service {
	options := ServiceOptions{OptionReader: store}
	if reader, ok := store.(port.ManagementRouteStrategyListReader); ok {
		options.ListReader = reader
	}
	if reader, ok := store.(port.ManagementRouteStrategyDetailReader); ok {
		options.DetailReader = reader
	}
	if writer, ok := store.(port.PublicRouteStrategyStore); ok {
		options.CreateStore = writer
	}
	if transactor, ok := store.(port.PublicRouteStrategyTransactor); ok {
		options.Transactor = transactor
	}
	return NewServiceWithOptions(options)
}

func NewServiceWithOptions(options ServiceOptions) *Service {
	createStore := options.CreateStore
	if createStore == nil {
		if candidate, ok := options.OptionReader.(port.PublicRouteStrategyStore); ok {
			createStore = candidate
		}
	}
	transactor := options.Transactor
	if transactor == nil {
		if candidate, ok := createStore.(port.PublicRouteStrategyTransactor); ok {
			transactor = candidate
		}
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	newID := options.NewID
	if newID == nil {
		newID = func(prefix string) string {
			return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
	}
	return &Service{
		store:        options.OptionReader,
		listReader:   options.ListReader,
		detailReader: options.DetailReader,
		createStore:  createStore,
		transactor:   transactor,
		invalidator:  options.Invalidator,
		logger:       logger,
		now:          now,
		newID:        newID,
	}
}

func (s *Service) Options(ctx context.Context, input OptionListInput) ([]Option, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management route strategy option store is required")
	}
	rows, err := s.store.ListManagementRouteStrategyOptions(ctx, port.ManagementRouteStrategyOptionListInput{
		SystemAccountID:            strings.TrimSpace(input.SystemAccountID),
		IncludeSystemAccountFields: input.IncludeSystemAccountFields,
		IDs:                        uniqueStrings(input.IDs, 50),
		Keyword:                    strings.TrimSpace(input.Keyword),
		Limit:                      optionLimit(input.Limit),
		ActiveOnly:                 input.ActiveOnly,
	})
	if err != nil {
		return nil, err
	}
	items := make([]Option, 0, len(rows))
	for _, row := range rows {
		items = append(items, Option{
			ID:                row.ID,
			SystemAccountID:   row.SystemAccountID,
			SystemAccountName: row.SystemAccountName,
			Name:              row.Name,
			Mode:              row.Mode,
			Status:            row.Status,
			IsDefault:         row.IsDefault,
		})
	}
	return items, nil
}

func optionLimit(limit int) int {
	if limit <= 0 {
		return defaultOptionLimit
	}
	return min(limit, maxOptionLimit)
}

func uniqueStrings(values []string, maxItems int) []string {
	seen := make(map[string]struct{}, len(values))
	output := make([]string, 0, len(values))
	for _, value := range values {
		text := strings.TrimSpace(value)
		if text == "" {
			continue
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		output = append(output, text)
		if len(output) >= maxItems {
			break
		}
	}
	return output
}
