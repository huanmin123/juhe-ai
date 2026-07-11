package systemsettings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"juhe-ai/backend-go/internal/timezonecompat"
)

var ErrPatchEmpty = errors.New("系统设置更新不能为空")

type ValidationError struct {
	Key     string
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}

type Entry struct {
	Key   string
	Value json.RawMessage
}

type Snapshot struct {
	values map[string]json.RawMessage
}

type Patch struct {
	values map[string]json.RawMessage
}

func NewSnapshot(values map[string]json.RawMessage) (Snapshot, error) {
	normalized, err := normalizeValues(values, true)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{values: normalized}, nil
}

func NewSnapshotFromEntries(entries []Entry) (Snapshot, error) {
	values := make(map[string]json.RawMessage, len(entries))
	for _, entry := range entries {
		if _, exists := values[entry.Key]; exists {
			return Snapshot{}, validationError(entry.Key, "系统设置字段重复："+entry.Key)
		}
		values[entry.Key] = cloneRaw(entry.Value)
	}
	return NewSnapshot(values)
}

func NewPatch(values map[string]json.RawMessage) (Patch, error) {
	if len(values) == 0 {
		return Patch{}, ErrPatchEmpty
	}
	normalized, err := normalizeValues(values, false)
	if err != nil {
		return Patch{}, err
	}
	return Patch{values: normalized}, nil
}

func (s Snapshot) Validate() error {
	_, err := NewSnapshot(s.values)
	return err
}

func (s Snapshot) Clone() Snapshot {
	return Snapshot{values: cloneValues(s.values)}
}

func (s Snapshot) Len() int {
	return len(s.values)
}

func (s Snapshot) Has(key string) bool {
	_, ok := s.values[key]
	return ok
}

func (s Snapshot) Value(key string) (json.RawMessage, bool) {
	value, ok := s.values[key]
	return cloneRaw(value), ok
}

func (s Snapshot) Values() map[string]json.RawMessage {
	return cloneValues(s.values)
}

func (s Snapshot) Entries() []Entry {
	return stableEntries(s.values)
}

func (s Snapshot) Apply(patch Patch) (Snapshot, error) {
	if err := s.Validate(); err != nil {
		return Snapshot{}, err
	}
	if err := patch.Validate(); err != nil {
		return Snapshot{}, err
	}
	values := s.Values()
	for key, value := range patch.values {
		values[key] = cloneRaw(value)
	}
	return NewSnapshot(values)
}

func (s Snapshot) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return marshalStableObject(s.values)
}

func (p Patch) Validate() error {
	if len(p.values) == 0 {
		return ErrPatchEmpty
	}
	_, err := NewPatch(p.values)
	return err
}

func (p Patch) Clone() Patch {
	return Patch{values: cloneValues(p.values)}
}

func (p Patch) Len() int {
	return len(p.values)
}

func (p Patch) Has(key string) bool {
	_, ok := p.values[key]
	return ok
}

func (p Patch) Value(key string) (json.RawMessage, bool) {
	value, ok := p.values[key]
	return cloneRaw(value), ok
}

func (p Patch) Values() map[string]json.RawMessage {
	return cloneValues(p.values)
}

func (p Patch) Entries() []Entry {
	return stableEntries(p.values)
}

func (p Patch) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return marshalStableObject(p.values)
}

func normalizeValues(values map[string]json.RawMessage, requireComplete bool) (map[string]json.RawMessage, error) {
	normalized := make(map[string]json.RawMessage, len(values))
	for _, key := range sortedMapKeys(values) {
		definition, ok := DefinitionFor(key)
		if !ok {
			return nil, validationError(key, "未知系统设置字段："+key)
		}
		value, err := normalizeValue(definition, values[key])
		if err != nil {
			return nil, err
		}
		normalized[key] = value
	}
	if requireComplete {
		for _, key := range sortedKeys {
			if _, ok := normalized[key]; !ok {
				return nil, validationError(key, "系统设置缺少字段："+key)
			}
		}
	}
	return normalized, nil
}

func normalizeValue(definition Definition, raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return nil, validationError(definition.Key, definition.Key+" 必须是有效 JSON")
	}
	switch definition.Kind {
	case ValueKindInteger:
		return normalizeInteger(definition, trimmed)
	case ValueKindDecimal:
		return normalizeDecimal(definition, trimmed)
	case ValueKindTimezone:
		return normalizeTimezone(definition.Key, trimmed)
	default:
		return nil, fmt.Errorf("unsupported system setting kind %q for %s", definition.Kind, definition.Key)
	}
}

func normalizeInteger(definition Definition, raw []byte) (json.RawMessage, error) {
	if !isJSONInteger(raw) {
		return nil, validationError(definition.Key, definition.Key+" 必须是整数")
	}
	value, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return nil, validationError(definition.Key, definition.Key+" 必须是整数")
	}
	if value < int64(definition.Minimum) || value > int64(definition.Maximum) {
		return nil, validationError(
			definition.Key,
			fmt.Sprintf("%s 必须在 %d 到 %d 之间", definition.Key, definition.Minimum, definition.Maximum),
		)
	}
	return json.RawMessage(strconv.FormatInt(value, 10)), nil
}

func normalizeDecimal(definition Definition, raw []byte) (json.RawMessage, error) {
	value, err := strconv.ParseFloat(string(raw), 64)
	if err != nil {
		return nil, validationError(definition.Key, definition.Key+" 必须是数字")
	}
	if value < definition.DecimalMinimum || value > definition.DecimalMaximum {
		return nil, validationError(
			definition.Key,
			fmt.Sprintf(
				"%s 必须在 %s 到 %s 之间",
				definition.Key,
				formatDecimal(definition.DecimalMinimum),
				formatDecimal(definition.DecimalMaximum),
			),
		)
	}
	return json.RawMessage(formatDecimal(value)), nil
}

func formatDecimal(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func normalizeTimezone(key string, raw []byte) (json.RawMessage, error) {
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, validationError(key, key+" 必须是非空 IANA 时区")
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil, validationError(key, key+" 必须是非空 IANA 时区")
	}
	if _, err := timezonecompat.LoadNodeLocation(normalized); err != nil {
		return nil, validationError(key, key+" 必须是有效 IANA 时区："+err.Error())
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("marshal system setting %s: %w", key, err)
	}
	return encoded, nil
}

func isJSONInteger(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	start := 0
	if raw[0] == '-' {
		if len(raw) == 1 {
			return false
		}
		start = 1
	}
	if raw[start] == '0' {
		return start == len(raw)-1
	}
	if raw[start] < '1' || raw[start] > '9' {
		return false
	}
	for index := start + 1; index < len(raw); index++ {
		if raw[index] < '0' || raw[index] > '9' {
			return false
		}
	}
	return true
}

func stableEntries(values map[string]json.RawMessage) []Entry {
	entries := make([]Entry, 0, len(values))
	for _, key := range sortedKeys {
		if value, ok := values[key]; ok {
			entries = append(entries, Entry{Key: key, Value: cloneRaw(value)})
		}
	}
	return entries
}

func marshalStableObject(values map[string]json.RawMessage) ([]byte, error) {
	var output bytes.Buffer
	output.WriteByte('{')
	for index, entry := range stableEntries(values) {
		if index > 0 {
			output.WriteByte(',')
		}
		key, err := json.Marshal(entry.Key)
		if err != nil {
			return nil, err
		}
		output.Write(key)
		output.WriteByte(':')
		output.Write(entry.Value)
	}
	output.WriteByte('}')
	return output.Bytes(), nil
}

func sortedMapKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneValues(values map[string]json.RawMessage) map[string]json.RawMessage {
	if values == nil {
		return nil
	}
	output := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		output[key] = cloneRaw(value)
	}
	return output
}

func cloneRaw(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func validationError(key string, message string) error {
	return &ValidationError{Key: key, Message: message}
}
