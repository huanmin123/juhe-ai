package gatewayopenai

import "testing"

func TestModelsEqualAndCanonicalModel(t *testing.T) {
	if !ModelsEqual(" GPT-4O ", "gpt-4o") {
		t.Fatal("model comparison should ignore case and surrounding spaces")
	}
	if got := CanonicalModel("GPT-4O", []string{"gpt-4o"}); got != "gpt-4o" {
		t.Fatalf("canonical model = %q", got)
	}
}

func TestResolveAccountModelMappingIgnoresSourceModelCase(t *testing.T) {
	enabled := true
	mapping := ResolveAccountModelMapping(&RuntimeAccount{ModelMappings: []AccountModelMapping{{
		SourceModel: "gpt-4o", SourceEndpointFamily: FamilyChatCompletions,
		UpstreamModel: "gpt-4o-mini", UpstreamEndpointFamily: FamilyChatCompletions,
		Enabled: &enabled,
	}}}, "GPT-4O", FamilyChatCompletions)
	if mapping == nil || mapping.UpstreamModel != "gpt-4o-mini" {
		t.Fatalf("mapping = %#v", mapping)
	}
}
