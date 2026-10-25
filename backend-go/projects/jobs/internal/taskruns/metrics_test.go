package taskruns

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/prommetrics"
)

func assertMetricBody(t *testing.T, registry *prommetrics.Registry, assert func(what string, body string)) {
	t.Helper()
	body, err := registry.GatherText()
	if err != nil {
		t.Fatal(err)
	}
	assert("metrics exposition", body)
}
