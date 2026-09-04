package gatewaycodex

// Port of codex-responses/contract-types.ts + contract-registry.ts.
//
// Request-side history normalization only needs the upstream item's ID
// prefix. Response shape and lifecycle validation deliberately do not live
// here (verified against the Node comment).

// CodexContractRevision mirrors CodexContractRevision.
type CodexContractRevision = string

// CodexResponsesContractRevision mirrors codexResponsesContractRevision.
const CodexResponsesContractRevision CodexContractRevision = "codex-responses-2026-07-11-r1"

// CodexItemContract mirrors CodexItemContract.
type CodexItemContract struct {
	Type   string
	Prefix string
}

// CodexResponsesContractRegistry mirrors CodexResponsesContractRegistry.
type CodexResponsesContractRegistry struct {
	Revision CodexContractRevision
	Items    []CodexItemContract

	byType map[string]CodexItemContract
}

// CodexResponsesContractRegistryItem mirrors registry.item(type).
func (r *CodexResponsesContractRegistry) Item(itemType string) (CodexItemContract, bool) {
	if r == nil {
		return CodexItemContract{}, false
	}
	item, ok := r.byType[itemType]
	return item, ok
}

var defaultCodexResponsesContractRegistry = CreateCodexResponsesContractRegistry(
	CodexResponsesContractRevision,
	[]CodexItemContract{
		itemContract("additional_tools", "at"),
		itemContract("message", "msg"),
		itemContract("agent_message", "amsg"),
		itemContract("reasoning", "rs"),
		itemContract("local_shell_call", "lsh"),
		itemContract("function_call", "fc"),
		itemContract("tool_search_call", "tsc"),
		itemContract("function_call_output", "fco"),
		itemContract("custom_tool_call", "ctc"),
		itemContract("custom_tool_call_output", "ctco"),
		itemContract("tool_search_output", "tso"),
		itemContract("web_search_call", "ws"),
		itemContract("image_generation_call", "ig"),
		itemContract("compaction", "cmp"),
		itemContract("compaction_summary", "cmp"),
		itemContract("context_compaction", "cmp"),
		itemWithoutID("compaction_trigger"),
	},
)

// CodexResponsesContractRegistryDefault mirrors the module-level
// codexResponsesContractRegistry export. The returned registry is shared;
// callers must not mutate Items.
func CodexResponsesContractRegistryDefault() *CodexResponsesContractRegistry {
	return defaultCodexResponsesContractRegistry
}

// CreateCodexResponsesContractRegistry mirrors createCodexResponsesContractRegistry.
func CreateCodexResponsesContractRegistry(revision CodexContractRevision, definitions []CodexItemContract) *CodexResponsesContractRegistry {
	items := make([]CodexItemContract, len(definitions))
	copy(items, definitions)
	byType := make(map[string]CodexItemContract, len(items))
	for _, definition := range items {
		byType[definition.Type] = definition
	}
	return &CodexResponsesContractRegistry{Revision: revision, Items: items, byType: byType}
}

func itemContract(itemType, prefix string) CodexItemContract {
	return CodexItemContract{Type: itemType, Prefix: prefix}
}

func itemWithoutID(itemType string) CodexItemContract {
	return CodexItemContract{Type: itemType}
}
