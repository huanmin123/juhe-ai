// httphelpers.go holds the HTTP-layer helpers shared by the policyreads
// domains: the store error message mapping, the mutation guard actor resolver
// and the 201 created response envelope.
package policyreads

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

func storeErrorMessage(err error, fallback string) string {
	var validation *ValidationError
	var conflict *ConflictError
	if errors.As(err, &validation) {
		return validation.Message
	}
	if errors.As(err, &conflict) {
		return conflict.Message
	}
	if err.Error() != "" && !errors.Is(err, errUnknownStoreFailure) {
		return err.Error()
	}
	return fallback
}

var errUnknownStoreFailure = errors.New("unknown store failure")

func policyreadsActorResolver(r *http.Request) string {
	if auth := authsys.AuthContextFrom(r); auth != nil {
		return auth.SystemAccountID
	}
	return "anonymous"
}

// createdEnvelope mirrors res.status(201).json(ok(data)).
type createdEnvelope struct {
	Data any `json:"data"`
}

func writeCreatedOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(createdEnvelope{Data: data})
}

func firstQueryValue(query map[string][]string, key string) (string, bool) {
	values, exists := query[key]
	if !exists || len(values) == 0 {
		return "", false
	}
	return values[0], true
}
