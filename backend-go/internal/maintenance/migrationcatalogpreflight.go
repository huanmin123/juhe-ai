package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"juhe-ai/backend-go/internal/migrationcatalog"
)

type MigrationCatalogPreflightResult struct {
	Success        bool     `json:"success"`
	Directory      string   `json:"directory"`
	MigrationCount int      `json:"migrationCount"`
	MinVersion     int64    `json:"minVersion"`
	MaxVersion     int64    `json:"maxVersion"`
	Issues         []string `json:"issues,omitempty"`
}

func RunMigrationCatalogPreflight(ctx context.Context, directory string, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	result := MigrationCatalogPreflightResult{
		Success:   true,
		Directory: filepath.Clean(directory),
	}
	var catalog migrationcatalog.Catalog
	var err error
	if directory == "" {
		result.Success = false
		result.Issues = []string{"migration catalog directory is required"}
	} else if catalog, err = migrationcatalog.Inspect(os.DirFS(result.Directory)); err != nil {
		result.Success = false
		result.Issues = []string{err.Error()}
	} else if len(catalog.Entries) == 0 {
		result.Success = false
		result.Issues = []string{"migration catalog is empty"}
	} else {
		result.MigrationCount = len(catalog.Entries)
		result.MinVersion = catalog.Entries[0].Version
		result.MaxVersion = catalog.Entries[len(catalog.Entries)-1].Version
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if encodeErr := json.NewEncoder(out).Encode(result); encodeErr != nil {
		return encodeErr
	}
	if !result.Success {
		return fmt.Errorf("migration catalog preflight 未通过")
	}
	return nil
}
