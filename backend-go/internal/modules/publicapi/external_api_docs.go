package publicapi

import (
	_ "embed"
	"encoding/json"
)

//go:embed external_public_api_catalog.generated.json
var embeddedAPIDocsCatalog string

func init() {
	if !json.Valid([]byte(embeddedAPIDocsCatalog)) {
		panic("publicapi: embedded external public API catalog is not valid JSON")
	}
}

// APIDocsCatalog returns the embedded external public API documentation snapshot.
func APIDocsCatalog() json.RawMessage {
	catalog := make(json.RawMessage, len(embeddedAPIDocsCatalog))
	copy(catalog, embeddedAPIDocsCatalog)
	return catalog
}
