package gateway

import "strings"

var definitions = [...]Definition{
	{ID: "openai-v1", Code: ProtocolOpenAI, Version: "v1", ResponseProtocol: ResponseProtocolOpenAIV1, ClientErrorProtocol: ClientErrorOpenAI, DefaultClientProfile: ClientProfileGenericOpenAI},
	{ID: "anthropic-v1", Code: ProtocolAnthropic, Version: "v1", ResponseProtocol: ResponseProtocolAnthropicV1, ClientErrorProtocol: ClientErrorAnthropic, DefaultClientProfile: ClientProfileGenericAnthropic},
	{ID: "gemini-v1beta", Code: ProtocolGemini, Version: "v1beta", ResponseProtocol: ResponseProtocolGeminiV1Beta, ClientErrorProtocol: ClientErrorGemini, DefaultClientProfile: ClientProfileGenericGemini},
}

func ListDefinitions() []Definition {
	result := make([]Definition, len(definitions))
	copy(result, definitions[:])
	return result
}

func DefinitionForProfile(profile Profile) (Definition, bool) {
	code := strings.ToLower(strings.TrimSpace(profile.Code))
	version := strings.ToLower(strings.TrimSpace(profile.Version))
	for _, definition := range definitions {
		if string(definition.Code) == code && definition.Version == version {
			return definition, true
		}
	}
	return Definition{}, false
}

func ResolveDefinition(request RequestShape, profile *Profile) (Definition, bool) {
	if protocol, ok := NativeProtocolForRequest(request); ok {
		return definitionForCode(protocol)
	}
	if profile != nil {
		return DefinitionForProfile(*profile)
	}
	return Definition{}, false
}

func definitionForCode(code ProtocolCode) (Definition, bool) {
	for _, definition := range definitions {
		if definition.Code == code {
			return definition, true
		}
	}
	return Definition{}, false
}
