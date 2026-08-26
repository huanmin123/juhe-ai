package modelcheckprobe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

func TestRunBasicProbeComposesTransportAndEvaluation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK"}`))
	}))
	defer server.Close()
	item, err := RunBasicProbe(context.Background(), BasicProbeInput{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", Prompt: "hello", MaxOutputTokens: 32})
	if err != nil || item.Status != "passed" || item.Score != 10 || item.ItemType != "responses_basic" {
		t.Fatalf("item=%#v err=%v", item, err)
	}
}
