// Package codexresponses validates the protocol contract of Codex Responses
// documents without taking ownership of repair, account policy, or HTTP I/O.
package codexresponses

import "strings"

const (
	Revision        = "codex-responses-2026-07-11-r1"
	DiagnosticLimit = 32
)

type Provenance string

const (
	ProvenanceRequestHistory Provenance = "request_history"
	ProvenanceRawUpstream    Provenance = "raw_upstream"
	ProvenanceGatewayBridge  Provenance = "gateway_bridge"
	ProvenanceUnknown        Provenance = "unknown"
)

type Outcome string

const (
	OutcomeClean           Outcome = "clean"
	OutcomeRepairable      Outcome = "repairable"
	OutcomeRepairedSafe    Outcome = "repaired_safe"
	OutcomeRepairedBridge  Outcome = "repaired_bridge"
	OutcomeBlocked         Outcome = "blocked"
	OutcomeObservedUnknown Outcome = "observed_unknown"
	OutcomeLateViolation   Outcome = "late_violation"
)

type Mode string

const (
	ModeShadow          Mode = "shadow"
	ModeSafeRepair      Mode = "safe_repair"
	ModeStrictIntercept Mode = "strict_intercept"
)

type CommitState struct {
	TransportCommitted bool
	SemanticCommitted  bool
	DownstreamBytes    int64
}

func (state CommitState) CanRetryUpstream() bool {
	return !state.TransportCommitted && !state.SemanticCommitted && state.DownstreamBytes <= 0
}

func OutcomeAtCommit(outcome Outcome, state CommitState) Outcome {
	if state.SemanticCommitted && (outcome == OutcomeRepairable || outcome == OutcomeBlocked) {
		return OutcomeLateViolation
	}
	return outcome
}

type RepairLevel string

const (
	RepairNone RepairLevel = ""
	RepairR0   RepairLevel = "R0"
	RepairR2   RepairLevel = "R2"
)

type FieldKind string

const (
	FieldPresent          FieldKind = "present"
	FieldString           FieldKind = "string"
	FieldArray            FieldKind = "array"
	FieldObject           FieldKind = "object"
	FieldEnum             FieldKind = "enum"
	FieldFunctionOutput   FieldKind = "function_output"
	FieldLocalShellAction FieldKind = "local_shell_action"
)

type Field struct {
	Name     string
	Kind     FieldKind
	Nullable bool
	Values   []string
}

type ItemContract struct {
	Type              string
	Prefix            string
	EventStages       []string
	RepairableIDPaths []string
	RequiredFields    []Field
	OptionalFields    []Field
}

type Registry struct {
	Revision RevisionType
	items    []ItemContract
	byType   map[string]ItemContract
	byPrefix map[string]ItemContract
}

type RevisionType string

func NewRegistry() Registry {
	items := []ItemContract{
		item("additional_tools", "at", []string{"added", "done"}, fields(field("role", FieldString), field("tools", FieldArray)), nil),
		item("message", "msg", []string{"added", "delta", "done"}, fields(field("role", FieldString), field("content", FieldArray)), fields(nullableField("phase", FieldEnum, "commentary", "final_answer"))),
		item("agent_message", "amsg", []string{"added", "delta", "done"}, fields(field("author", FieldString), field("recipient", FieldString), field("content", FieldArray)), nil),
		item("reasoning", "rs", []string{"added", "delta", "done"}, fields(field("summary", FieldArray)), fields(nullableField("content", FieldArray), nullableField("encrypted_content", FieldString))),
		item("local_shell_call", "lsh", []string{"added", "done"}, fields(fieldEnum("status", "completed", "in_progress", "incomplete"), field("action", FieldLocalShellAction)), fields(nullableField("call_id", FieldString))),
		item("function_call", "fc", []string{"added", "delta", "done"}, fields(field("name", FieldString), field("arguments", FieldString), field("call_id", FieldString)), fields(nullableField("namespace", FieldString))),
		item("tool_search_call", "tsc", []string{"added", "done"}, fields(field("execution", FieldString), field("arguments", FieldPresent)), fields(nullableField("call_id", FieldString), nullableField("status", FieldString))),
		item("function_call_output", "fco", []string{"added", "done"}, fields(field("call_id", FieldString), field("output", FieldFunctionOutput)), nil),
		item("custom_tool_call", "ctc", []string{"added", "delta", "done"}, fields(field("call_id", FieldString), field("name", FieldString), field("input", FieldString)), fields(nullableField("status", FieldString), nullableField("namespace", FieldString))),
		item("custom_tool_call_output", "ctco", []string{"added", "done"}, fields(field("call_id", FieldString), field("output", FieldFunctionOutput)), fields(nullableField("name", FieldString))),
		item("tool_search_output", "tso", []string{"added", "done"}, fields(field("status", FieldString), field("execution", FieldString), field("tools", FieldArray)), fields(nullableField("call_id", FieldString))),
		item("web_search_call", "ws", []string{"added", "done"}, nil, fields(nullableField("status", FieldString), nullableField("action", FieldObject))),
		item("image_generation_call", "ig", []string{"added", "done"}, fields(field("status", FieldString), field("result", FieldString)), fields(nullableField("revised_prompt", FieldString))),
		item("compaction", "cmp", []string{"added", "done"}, fields(field("encrypted_content", FieldString)), nil),
		item("compaction_summary", "cmp", []string{"added", "done"}, fields(field("encrypted_content", FieldString)), nil),
		item("context_compaction", "cmp", []string{"added", "done"}, nil, fields(nullableField("encrypted_content", FieldString))),
		{Type: "compaction_trigger", EventStages: []string{}, RepairableIDPaths: []string{}},
	}
	byType := make(map[string]ItemContract, len(items))
	byPrefix := make(map[string]ItemContract)
	for _, value := range items {
		value = cloneItem(value)
		byType[value.Type] = value
		if value.Prefix != "" {
			if _, exists := byPrefix[value.Prefix]; !exists {
				byPrefix[value.Prefix] = value
			}
		}
	}
	return Registry{Revision: Revision, items: items, byType: byType, byPrefix: byPrefix}
}

func (r Registry) Items() []ItemContract {
	result := make([]ItemContract, len(r.items))
	for index, value := range r.items {
		result[index] = cloneItem(value)
	}
	return result
}

func (r Registry) Item(itemType string) (ItemContract, bool) {
	value, ok := r.byType[itemType]
	return cloneItem(value), ok
}

func (r Registry) ItemByPrefix(prefix string) (ItemContract, bool) {
	value, ok := r.byPrefix[prefix]
	return cloneItem(value), ok
}

func item(itemType, prefix string, stages []string, required, optional []Field) ItemContract {
	common := Field{Name: "internal_chat_message_metadata_passthrough", Kind: FieldObject, Nullable: true}
	optional = append(optional, common)
	return ItemContract{Type: itemType, Prefix: prefix, EventStages: stages, RepairableIDPaths: []string{"id"}, RequiredFields: required, OptionalFields: optional}
}

func field(name string, kind FieldKind) Field { return Field{Name: name, Kind: kind} }

func nullableField(name string, kind FieldKind, values ...string) Field {
	return Field{Name: name, Kind: kind, Nullable: true, Values: append([]string(nil), values...)}
}

func fieldEnum(name string, values ...string) Field {
	return Field{Name: name, Kind: FieldEnum, Values: append([]string(nil), values...)}
}

func fields(values ...Field) []Field { return values }

func cloneItem(value ItemContract) ItemContract {
	if value.Type == "" {
		return value
	}
	value.EventStages = append([]string(nil), value.EventStages...)
	value.RepairableIDPaths = append([]string(nil), value.RepairableIDPaths...)
	value.RequiredFields = cloneFields(value.RequiredFields)
	value.OptionalFields = cloneFields(value.OptionalFields)
	return value
}

func cloneFields(values []Field) []Field {
	result := make([]Field, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Values = append([]string(nil), value.Values...)
	}
	return result
}

func isExpectedID(id, prefix string) bool {
	return prefix != "" && strings.HasPrefix(id, prefix+"_") && len(id) > len(prefix)+1
}
