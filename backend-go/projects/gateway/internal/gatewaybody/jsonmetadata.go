package gatewaybody

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Byte-level JSON metadata scanner, mirroring request/json-metadata-scanner.ts.
// The scanner walks the raw bytes without building a DOM: it validates the
// document with an explicit frame stack and invokes property callbacks for
// the top-level metadata keys while parsing, so oversized bodies can be
// inspected without materializing the whole object.

// JSONBodyMetadata mirrors GatewayJsonBodyMetadata. Model / Stream /
// ServiceTier / ReasoningEffort / MaxOutputTokens keep pointers because the
// Node scanner may leave them undefined even on invalid JSON (partial reads);
// the boolean fields mirror the Node final assembly, which always assigns
// them once the document is valid.
type JSONBodyMetadata struct {
	Model                   *string
	Stream                  *bool
	ServiceTier             *string
	ReasoningEffort         *string
	MaxOutputTokens         *int
	ImageGeneration         bool
	ImageGenerationForced   bool
	StrictOutputRequirement bool
	CodexCompactionTrigger  bool
	InvalidJSON             bool
}

type generationConfigInspection struct {
	nextIndex       int
	isObject        bool
	reasoningEffort *string
	imageOutput     bool
}

type jsonToolInspectionReadResult struct {
	nextIndex  int
	inspection ImageGenerationToolInspection
	required   bool
}

type jsonReadResult struct {
	nextIndex int
	ok        bool
}

type jsonStringToken struct {
	value     string
	nextIndex int
}

type jsonObjectStringPropertyResult struct {
	nextIndex int
	value     *string
	ok        bool
}

// ExtractJSONBodyMetadata mirrors extractGatewayJsonBodyMetadata.
func ExtractJSONBodyMetadata(raw []byte) JSONBodyMetadata {
	var metadata JSONBodyMetadata
	codexCompactionTrigger := false
	var reasoningObjectEffort *string
	var reasoningFieldEffort *string
	var outputConfigEffort *string
	var camelGenerationConfig *generationConfigInspection
	var snakeGenerationConfig *generationConfigInspection
	var maxOutputTokens *int
	var maxTokens *int
	topLevelTypeForcesImage := false
	toolsInspection := emptyImageGenerationToolInspection()
	toolChoiceInspection := emptyImageGenerationToolInspection()
	toolChoiceRequired := false
	responseFormatStrict := false
	toolsStrict := false
	toolChoiceStrict := false

	valid := isValidJSONDocument(raw, &jsonDocumentCallbacks{
		onCodexCompactionTrigger: func() {
			codexCompactionTrigger = true
		},
		onTopLevelProperty: func(key string, index int, _ int) {
			switch key {
			case "model":
				if value, ok := readJSONStringToken(raw, index); ok {
					metadata.Model = &value.value
				} else {
					metadata.Model = nil
				}
			case "service_tier":
				if value, ok := readJSONStringToken(raw, index); ok {
					if tier, ok := NormalizeOptionalUsageServiceTier(value.value); ok {
						metadata.ServiceTier = &tier
					} else {
						metadata.ServiceTier = nil
					}
				} else {
					metadata.ServiceTier = nil
				}
			case "reasoning_effort":
				if value, ok := readJSONStringToken(raw, index); ok {
					if effort, ok := NormalizeUsageReasoningEffort(value.value); ok {
						reasoningFieldEffort = &effort
					} else {
						reasoningFieldEffort = nil
					}
				} else {
					reasoningFieldEffort = nil
				}
			case "reasoning":
				result := readJSONObjectStringProperty(raw, index, "effort")
				reasoningObjectEffort = normalizeEffortPointer(result.value)
			case "output_config":
				result := readJSONObjectStringProperty(raw, index, "effort")
				outputConfigEffort = normalizeEffortPointer(result.value)
			case "generationConfig":
				camelGenerationConfig = inspectGenerationConfig(raw, index)
			case "generation_config":
				snakeGenerationConfig = inspectGenerationConfig(raw, index)
			case "stream":
				if value, ok := readJSONBoolean(raw, index); ok {
					metadata.Stream = &value
				} else {
					metadata.Stream = nil
				}
			case "max_output_tokens":
				if value, ok := readJSONNonNegativeInteger(raw, index); ok {
					metadata.MaxOutputTokens = &value
				} else {
					metadata.MaxOutputTokens = nil
				}
			case "max_tokens":
				if value, ok := readJSONNonNegativeInteger(raw, index); ok {
					maxTokens = &value
				} else {
					maxTokens = nil
				}
			case "type":
				if value, ok := readJSONStringToken(raw, index); ok {
					topLevelTypeForcesImage = value.value == "image_generation"
				} else {
					topLevelTypeForcesImage = false
				}
			case "tools":
				toolsStrict = jsonValueIsTruthy(raw, index)
				toolsInspection = inspectJSONToolDefinitions(raw, index, 0).inspection
			case "tool_choice":
				toolChoiceStrict = jsonValueIsTruthy(raw, index)
				result := inspectJSONToolChoice(raw, index)
				toolChoiceInspection = result.inspection
				toolChoiceRequired = result.required
			case "response_format":
				responseFormatStrict = jsonValueIsTruthy(raw, index)
			}
		},
	})

	if !valid {
		metadata.InvalidJSON = true
		return metadata
	}

	var generationConfig *generationConfigInspection
	if camelGenerationConfig != nil && camelGenerationConfig.isObject {
		generationConfig = camelGenerationConfig
	} else if snakeGenerationConfig != nil && snakeGenerationConfig.isObject {
		generationConfig = snakeGenerationConfig
	}
	inspection := emptyImageGenerationToolInspection()
	mergeImageGenerationToolInspection(&inspection, &toolsInspection)
	mergeImageGenerationToolInspection(&inspection, &toolChoiceInspection)
	inspection.ForcedImageGeneration = inspection.ForcedImageGeneration || topLevelTypeForcesImage
	if toolChoiceRequired && inspection.ImageToolCount > 0 && inspection.NonImageToolCount == 0 {
		inspection.ForcedImageGeneration = true
	}

	metadata.ReasoningEffort = firstStringPointer(reasoningObjectEffort, reasoningFieldEffort, outputConfigEffort, generationConfigEffort(generationConfig))
	var tokenLimits []*int
	if maxOutputTokens != nil {
		tokenLimits = append(tokenLimits, maxOutputTokens)
	}
	if maxTokens != nil {
		tokenLimits = append(tokenLimits, maxTokens)
	}
	if len(tokenLimits) > 0 {
		best := *tokenLimits[0]
		for _, value := range tokenLimits[1:] {
			if *value > best {
				best = *value
			}
		}
		metadata.MaxOutputTokens = &best
	}
	metadata.ImageGeneration = (generationConfig != nil && generationConfig.imageOutput) ||
		inspection.ImageToolCount > 0 ||
		inspection.ForcedImageGeneration
	metadata.ImageGenerationForced = inspection.ForcedImageGeneration
	metadata.StrictOutputRequirement = responseFormatStrict || toolsStrict || toolChoiceStrict
	metadata.CodexCompactionTrigger = codexCompactionTrigger
	return metadata
}

func generationConfigEffort(config *generationConfigInspection) *string {
	if config == nil {
		return nil
	}
	return config.reasoningEffort
}

func normalizeEffortPointer(value *string) *string {
	if value == nil {
		return nil
	}
	if effort, ok := NormalizeUsageReasoningEffort(*value); ok {
		return &effort
	}
	return nil
}

func firstStringPointer(values ...*string) *string {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func inspectGenerationConfig(raw []byte, index int) *generationConfigInspection {
	index = skipJSONWhitespace(raw, index)
	if index >= len(raw) || raw[index] != jsonObjectOpenByte {
		skipped := skipJSONValue(raw, index)
		return &generationConfigInspection{nextIndex: skipped.nextIndex, imageOutput: false}
	}

	index++
	var camelThinkingIsObject bool
	var camelThinkingEffort *string
	var snakeThinkingIsObject bool
	var snakeThinkingEffort *string
	var camelModalitiesDefined bool
	var camelModalitiesImage bool
	var snakeModalitiesImage bool
	var camelMimeDefined bool
	var camelMimeImage bool
	var snakeMimeImage bool
	result := func(nextIndex int) *generationConfigInspection {
		var effort *string
		if camelThinkingIsObject {
			effort = camelThinkingEffort
		} else if snakeThinkingIsObject {
			effort = snakeThinkingEffort
		}
		modalitiesImage := snakeModalitiesImage
		if camelModalitiesDefined {
			modalitiesImage = camelModalitiesImage
		}
		mimeImage := snakeMimeImage
		if camelMimeDefined {
			mimeImage = camelMimeImage
		}
		return &generationConfigInspection{
			nextIndex:       nextIndex,
			isObject:        true,
			reasoningEffort: effort,
			imageOutput:     modalitiesImage || mimeImage,
		}
	}
	for index < len(raw) {
		index = skipJSONWhitespace(raw, index)
		if index < len(raw) && raw[index] == jsonObjectCloseByte {
			return result(index + 1)
		}
		if index < len(raw) && raw[index] == jsonCommaByte {
			index++
			continue
		}
		key, ok := readJSONStringToken(raw, index)
		if !ok {
			return result(len(raw))
		}
		index = skipJSONWhitespace(raw, key.nextIndex)
		if index >= len(raw) || raw[index] != jsonColonByte {
			return result(len(raw))
		}
		index = skipJSONWhitespace(raw, index+1)
		switch key.value {
		case "thinkingConfig":
			camelThinkingIsObject = index < len(raw) && raw[index] == jsonObjectOpenByte
			nested := readJSONObjectStringProperty(raw, index, "thinkingLevel")
			camelThinkingEffort = normalizeEffortPointer(nested.value)
			index = nested.nextIndex
			continue
		case "thinking_config":
			snakeThinkingIsObject = index < len(raw) && raw[index] == jsonObjectOpenByte
			nested := readJSONObjectStringProperty(raw, index, "thinking_level")
			snakeThinkingEffort = normalizeEffortPointer(nested.value)
			index = nested.nextIndex
			continue
		case "responseModalities":
			camelModalitiesDefined = !jsonValueIsNull(raw, index)
			modalitiesNextIndex, modalitiesImage := inspectJSONStringArrayForImage(raw, index)
			camelModalitiesImage = modalitiesImage
			index = modalitiesNextIndex
			continue
		case "response_modalities":
			modalitiesNextIndex, modalitiesImage := inspectJSONStringArrayForImage(raw, index)
			snakeModalitiesImage = modalitiesImage
			index = modalitiesNextIndex
			continue
		case "responseMimeType":
			camelMimeDefined = !jsonValueIsNull(raw, index)
			if value, ok := readJSONStringToken(raw, index); ok {
				camelMimeImage = imageMimePattern.MatchString(trimJSSpace(value.value))
				index = value.nextIndex
				continue
			}
			camelMimeImage = false
		case "response_mime_type":
			if value, ok := readJSONStringToken(raw, index); ok {
				snakeMimeImage = imageMimePattern.MatchString(trimJSSpace(value.value))
				index = value.nextIndex
				continue
			}
			snakeMimeImage = false
		}
		index = skipJSONValue(raw, index).nextIndex
	}
	return result(len(raw))
}

var imageMimePattern = regexp.MustCompile(`(?i)^image/`)

// trimJSSpace mirrors JavaScript String.prototype.trim(): the ECMAScript
// WhiteSpace + LineTerminator set, including U+FEFF which Go's
// strings.TrimSpace does not cover.
func trimJSSpace(value string) string {
	return strings.TrimFunc(value, func(r rune) bool {
		switch r {
		case 0x0009, 0x000B, 0x000C, 0x0020, 0x00A0, 0x1680, 0x202F, 0x205F, 0x3000,
			0x000A, 0x000D, 0x2028, 0x2029, 0xFEFF:
			return true
		}
		return r >= 0x2000 && r <= 0x200A
	})
}

func jsonValueIsNull(raw []byte, index int) bool {
	index = skipJSONWhitespace(raw, index)
	if index+len(jsonNullBuffer) > len(raw) {
		return false
	}
	return bytes.Equal(raw[index:index+len(jsonNullBuffer)], jsonNullBuffer)
}

var (
	jsonNullBuffer  = []byte("null")
	jsonTrueBuffer  = []byte("true")
	jsonFalseBuffer = []byte("false")
)

func inspectJSONStringArrayForImage(raw []byte, index int) (int, bool) {
	index = skipJSONWhitespace(raw, index)
	if index >= len(raw) || raw[index] != jsonArrayOpenByte {
		skipped := skipJSONValue(raw, index)
		return skipped.nextIndex, false
	}
	index++
	imageOutput := false
	for index < len(raw) {
		index = skipJSONWhitespace(raw, index)
		if index < len(raw) && raw[index] == jsonArrayCloseByte {
			return index + 1, imageOutput
		}
		if index < len(raw) && raw[index] == jsonCommaByte {
			index++
			continue
		}
		if value, ok := readJSONStringToken(raw, index); ok {
			if strings.ToLower(trimJSSpace(value.value)) == "image" {
				imageOutput = true
			}
			index = value.nextIndex
			continue
		}
		index = skipJSONValue(raw, index).nextIndex
	}
	return len(raw), imageOutput
}

func readJSONObjectStringProperty(raw []byte, index int, propertyName string) jsonObjectStringPropertyResult {
	return readJSONObjectNestedStringProperty(raw, index, []string{propertyName})
}

func readJSONObjectNestedStringProperty(raw []byte, index int, propertyPath []string) jsonObjectStringPropertyResult {
	index = skipJSONWhitespace(raw, index)
	if len(propertyPath) == 0 || index >= len(raw) || raw[index] != jsonObjectOpenByte {
		skipped := skipJSONValue(raw, index)
		return jsonObjectStringPropertyResult{nextIndex: skipped.nextIndex, ok: skipped.ok}
	}

	index++
	var value *string
	for index < len(raw) {
		index = skipJSONWhitespace(raw, index)
		if index < len(raw) && raw[index] == jsonObjectCloseByte {
			return jsonObjectStringPropertyResult{nextIndex: index + 1, value: value, ok: true}
		}
		if index < len(raw) && raw[index] == jsonCommaByte {
			index++
			continue
		}
		key, ok := readJSONStringToken(raw, index)
		if !ok {
			return jsonObjectStringPropertyResult{nextIndex: len(raw), value: value, ok: false}
		}
		index = skipJSONWhitespace(raw, key.nextIndex)
		if index >= len(raw) || raw[index] != jsonColonByte {
			return jsonObjectStringPropertyResult{nextIndex: len(raw), value: value, ok: false}
		}
		index = skipJSONWhitespace(raw, index+1)
		if key.value == propertyPath[0] {
			if len(propertyPath) == 1 {
				if propertyValue, ok := readJSONStringToken(raw, index); ok {
					value = &propertyValue.value
					index = propertyValue.nextIndex
					continue
				}
				value = nil
			} else {
				nested := readJSONObjectNestedStringProperty(raw, index, propertyPath[1:])
				if !nested.ok {
					return jsonObjectStringPropertyResult{nextIndex: nested.nextIndex, value: value, ok: false}
				}
				value = nested.value
				index = nested.nextIndex
				continue
			}
		}
		skipped := skipJSONValue(raw, index)
		if !skipped.ok {
			return jsonObjectStringPropertyResult{nextIndex: skipped.nextIndex, value: value, ok: false}
		}
		index = skipped.nextIndex
	}
	return jsonObjectStringPropertyResult{nextIndex: len(raw), value: value, ok: false}
}

func emptyImageGenerationToolInspection() ImageGenerationToolInspection {
	return ImageGenerationToolInspection{}
}

func mergeImageGenerationToolInspection(target *ImageGenerationToolInspection, source *ImageGenerationToolInspection) {
	target.ImageToolCount += source.ImageToolCount
	target.NonImageToolCount += source.NonImageToolCount
	target.ForcedImageGeneration = target.ForcedImageGeneration || source.ForcedImageGeneration
}

func inspectJSONToolDefinitions(raw []byte, index int, depth int) jsonToolInspectionReadResult {
	inspection := emptyImageGenerationToolInspection()
	index = skipJSONWhitespace(raw, index)
	if depth > 4 {
		skipped := skipJSONValue(raw, index)
		return jsonToolInspectionReadResult{nextIndex: skipped.nextIndex, inspection: inspection}
	}
	if index < len(raw) && raw[index] == jsonStringByte {
		value, ok := readJSONStringToken(raw, index)
		if !ok {
			return jsonToolInspectionReadResult{nextIndex: len(raw), inspection: inspection}
		}
		countToolType(value.value, &inspection)
		return jsonToolInspectionReadResult{nextIndex: value.nextIndex, inspection: inspection}
	}
	if index < len(raw) && raw[index] == jsonArrayOpenByte {
		index++
		for index < len(raw) {
			index = skipJSONWhitespace(raw, index)
			if index < len(raw) && raw[index] == jsonArrayCloseByte {
				return jsonToolInspectionReadResult{nextIndex: index + 1, inspection: inspection}
			}
			if index < len(raw) && raw[index] == jsonCommaByte {
				index++
				continue
			}
			result := inspectJSONToolDefinitions(raw, index, depth+1)
			mergeImageGenerationToolInspection(&inspection, &result.inspection)
			index = result.nextIndex
		}
		return jsonToolInspectionReadResult{nextIndex: len(raw), inspection: inspection}
	}
	if index < len(raw) && raw[index] == jsonObjectOpenByte {
		return inspectJSONToolObject(raw, index)
	}
	skipped := skipJSONValue(raw, index)
	return jsonToolInspectionReadResult{nextIndex: skipped.nextIndex, inspection: inspection}
}

func inspectJSONToolObject(raw []byte, index int) jsonToolInspectionReadResult {
	inspection := emptyImageGenerationToolInspection()
	var toolType *string
	index++
	for index < len(raw) {
		index = skipJSONWhitespace(raw, index)
		if index < len(raw) && raw[index] == jsonObjectCloseByte {
			if toolType != nil {
				countToolType(*toolType, &inspection)
			}
			return jsonToolInspectionReadResult{nextIndex: index + 1, inspection: inspection}
		}
		if index < len(raw) && raw[index] == jsonCommaByte {
			index++
			continue
		}
		key, ok := readJSONStringToken(raw, index)
		if !ok {
			return jsonToolInspectionReadResult{nextIndex: len(raw), inspection: inspection}
		}
		index = skipJSONWhitespace(raw, key.nextIndex)
		if index >= len(raw) || raw[index] != jsonColonByte {
			return jsonToolInspectionReadResult{nextIndex: len(raw), inspection: inspection}
		}
		index = skipJSONWhitespace(raw, index+1)
		if key.value == "type" {
			if value, ok := readJSONStringToken(raw, index); ok {
				copied := value.value
				toolType = &copied
				index = value.nextIndex
				continue
			}
			toolType = nil
		}
		index = skipJSONValue(raw, index).nextIndex
	}
	return jsonToolInspectionReadResult{nextIndex: len(raw), inspection: inspection}
}

func inspectJSONToolChoice(raw []byte, index int) jsonToolInspectionReadResult {
	inspection := emptyImageGenerationToolInspection()
	index = skipJSONWhitespace(raw, index)
	if index < len(raw) && raw[index] == jsonStringByte {
		value, ok := readJSONStringToken(raw, index)
		if !ok {
			return jsonToolInspectionReadResult{nextIndex: len(raw), inspection: inspection}
		}
		if value.value == "image_generation" {
			inspection.ForcedImageGeneration = true
		}
		return jsonToolInspectionReadResult{
			nextIndex:  value.nextIndex,
			inspection: inspection,
			required:   value.value == "required",
		}
	}
	if index >= len(raw) || raw[index] != jsonObjectOpenByte {
		skipped := skipJSONValue(raw, index)
		return jsonToolInspectionReadResult{nextIndex: skipped.nextIndex, inspection: inspection}
	}

	index++
	var choiceType *string
	nestedTools := emptyImageGenerationToolInspection()
	for index < len(raw) {
		index = skipJSONWhitespace(raw, index)
		if index < len(raw) && raw[index] == jsonObjectCloseByte {
			if choiceType != nil && *choiceType == "image_generation" {
				inspection.ForcedImageGeneration = true
			}
			mergeImageGenerationToolInspection(&inspection, &nestedTools)
			return jsonToolInspectionReadResult{nextIndex: index + 1, inspection: inspection}
		}
		if index < len(raw) && raw[index] == jsonCommaByte {
			index++
			continue
		}
		key, ok := readJSONStringToken(raw, index)
		if !ok {
			return jsonToolInspectionReadResult{nextIndex: len(raw), inspection: inspection}
		}
		index = skipJSONWhitespace(raw, key.nextIndex)
		if index >= len(raw) || raw[index] != jsonColonByte {
			return jsonToolInspectionReadResult{nextIndex: len(raw), inspection: inspection}
		}
		index = skipJSONWhitespace(raw, index+1)
		if key.value == "type" {
			if value, ok := readJSONStringToken(raw, index); ok {
				copied := value.value
				choiceType = &copied
				index = value.nextIndex
				continue
			}
			choiceType = nil
		} else if key.value == "tools" {
			result := inspectJSONToolDefinitions(raw, index, 0)
			nestedTools = result.inspection
			index = result.nextIndex
			continue
		}
		index = skipJSONValue(raw, index).nextIndex
	}
	return jsonToolInspectionReadResult{nextIndex: len(raw), inspection: inspection}
}

func countToolType(value string, inspection *ImageGenerationToolInspection) {
	if value == "image_generation" {
		inspection.ImageToolCount++
	} else if trimJSSpace(value) != "" {
		inspection.NonImageToolCount++
	}
}

func skipJSONWhitespace(raw []byte, index int) int {
	for index < len(raw) && isJSONWhitespaceByte(raw[index]) {
		index++
	}
	return index
}

func readJSONStringToken(raw []byte, index int) (jsonStringToken, bool) {
	nextIndex, ok := skipJSONString(raw, index)
	if !ok {
		return jsonStringToken{}, false
	}
	return decodeJSONStringToken(raw, index, nextIndex)
}

func decodeJSONStringToken(raw []byte, index int, nextIndex int) (jsonStringToken, bool) {
	if bytes.IndexByte(raw[index+1:nextIndex-1], jsonEscapeByte) < 0 {
		return jsonStringToken{value: string(raw[index+1 : nextIndex-1]), nextIndex: nextIndex}, true
	}
	var value string
	if err := json.Unmarshal(raw[index:nextIndex], &value); err != nil {
		return jsonStringToken{}, false
	}
	return jsonStringToken{value: value, nextIndex: nextIndex}, true
}

func readJSONBoolean(raw []byte, index int) (bool, bool) {
	if index+4 <= len(raw) && bytes.Equal(raw[index:index+4], jsonTrueBuffer) {
		return true, true
	}
	if index+5 <= len(raw) && bytes.Equal(raw[index:index+5], jsonFalseBuffer) {
		return false, true
	}
	return false, false
}

func readJSONNonNegativeInteger(raw []byte, index int) (int, bool) {
	result := skipJSONValue(raw, index)
	if !result.ok {
		return 0, false
	}
	text := trimJSSpace(string(raw[index:result.nextIndex]))
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	if value < 0 || value > maxSafeInteger || value != math.Trunc(value) {
		return 0, false
	}
	return int(value), true
}

func jsonValueIsTruthy(raw []byte, index int) bool {
	index = skipJSONWhitespace(raw, index)
	var byteValue byte
	if index < len(raw) {
		byteValue = raw[index]
	}
	if byteValue == jsonObjectOpenByte || byteValue == jsonArrayOpenByte {
		return true
	}
	if byteValue == jsonStringByte {
		if value, ok := readJSONStringToken(raw, index); ok {
			return value.value != ""
		}
		return false
	}
	if index+4 <= len(raw) && bytes.Equal(raw[index:index+4], jsonTrueBuffer) {
		return true
	}
	if (index+4 <= len(raw) && bytes.Equal(raw[index:index+4], jsonNullBuffer)) ||
		(index+5 <= len(raw) && bytes.Equal(raw[index:index+5], jsonFalseBuffer)) {
		return false
	}
	skipped := skipJSONValue(raw, index)
	if !skipped.ok {
		return false
	}
	text := trimJSSpace(string(raw[index:skipped.nextIndex]))
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return false
	}
	return !math.IsNaN(value) && value != 0
}

func skipJSONValue(raw []byte, index int) jsonReadResult {
	index = skipJSONWhitespace(raw, index)
	if index >= len(raw) {
		return jsonReadResult{nextIndex: index, ok: false}
	}
	firstByte := raw[index]
	if firstByte == jsonStringByte {
		nextIndex, ok := skipJSONString(raw, index)
		if !ok {
			return jsonReadResult{nextIndex: len(raw), ok: false}
		}
		return jsonReadResult{nextIndex: nextIndex, ok: true}
	}
	if firstByte != jsonObjectOpenByte && firstByte != jsonArrayOpenByte {
		startIndex := index
		for index < len(raw) &&
			raw[index] != jsonCommaByte &&
			raw[index] != jsonObjectCloseByte &&
			raw[index] != jsonArrayCloseByte {
			index++
		}
		return jsonReadResult{
			nextIndex: index,
			ok:        isValidJSONPrimitive(raw, startIndex, index),
		}
	}

	var stack compactJSONFrameStack
	stack.push(firstByte)
	for cursor := index + 1; cursor < len(raw); cursor++ {
		byteValue := raw[cursor]
		if byteValue == jsonStringByte {
			nextIndex, ok := skipJSONString(raw, cursor)
			if !ok {
				return jsonReadResult{nextIndex: len(raw), ok: false}
			}
			cursor = nextIndex - 1
			continue
		}
		if byteValue == jsonObjectOpenByte || byteValue == jsonArrayOpenByte {
			stack.push(byteValue)
			continue
		}
		if byteValue == jsonObjectCloseByte || byteValue == jsonArrayCloseByte {
			previous, hasPrevious := stack.pop()
			if !hasPrevious ||
				(byteValue == jsonObjectCloseByte && previous != jsonObjectOpenByte) ||
				(byteValue == jsonArrayCloseByte && previous != jsonArrayOpenByte) {
				return jsonReadResult{nextIndex: len(raw), ok: false}
			}
			if stack.length == 0 {
				return jsonReadResult{nextIndex: cursor + 1, ok: true}
			}
		}
	}
	return jsonReadResult{nextIndex: len(raw), ok: false}
}

func skipJSONString(raw []byte, index int) (int, bool) {
	if index >= len(raw) || raw[index] != jsonStringByte {
		return 0, false
	}
	escaped := false
	for cursor := index + 1; cursor < len(raw); cursor++ {
		byteValue := raw[cursor]
		if escaped {
			escaped = false
			continue
		}
		if byteValue == jsonEscapeByte {
			escaped = true
			continue
		}
		if byteValue == jsonStringByte {
			return cursor + 1, true
		}
	}
	return 0, false
}

// compactJSONFrameStack mirrors CompactJsonFrameStack: 256 byte frames,
// doubling on demand.
type compactJSONFrameStack struct {
	frames []byte
	length int
}

func (s *compactJSONFrameStack) push(frame byte) {
	if s.length >= len(s.frames) {
		capacity := len(s.frames)
		if capacity == 0 {
			capacity = 256
		} else {
			capacity *= 2
		}
		expanded := make([]byte, capacity)
		copy(expanded, s.frames[:s.length])
		s.frames = expanded
	}
	s.frames[s.length] = frame
	s.length++
}

func (s *compactJSONFrameStack) peek() (byte, bool) {
	if s.length <= 0 {
		return 0, false
	}
	return s.frames[s.length-1], true
}

func (s *compactJSONFrameStack) replaceTop(frame byte) bool {
	if s.length == 0 {
		return false
	}
	s.frames[s.length-1] = frame
	return true
}

func (s *compactJSONFrameStack) pop() (byte, bool) {
	if s.length == 0 {
		return 0, false
	}
	s.length--
	return s.frames[s.length], true
}

const (
	jsonFrameStateMask            = 0x07
	jsonFrameTypeIsCompactionFlag = 0x08
	jsonFrameKeyIsTypeFlag        = 0x10
	jsonObjectFirstKeyOrEndState  = 0
	jsonObjectKeyState            = 1
	jsonObjectColonState          = 2
	jsonObjectValueState          = 3
	jsonObjectCommaOrEndState     = 4
	jsonArrayFirstValueOrEndState = 5
	jsonArrayValueState           = 6
	jsonArrayCommaOrEndState      = 7
)

func jsonFrameState(frame byte) byte {
	return frame & jsonFrameStateMask
}

func jsonFrameWithState(frame byte, state byte) byte {
	return (frame & ^byte(jsonFrameStateMask)) | state
}

func isJSONObjectFrame(frame byte) bool {
	return jsonFrameState(frame) <= jsonObjectCommaOrEndState
}

type jsonDocumentCallbacks struct {
	onCodexCompactionTrigger func()
	onTopLevelProperty       func(key string, startIndex int, endIndex int)
}

// isValidJSONDocument mirrors isValidJsonDocument, including the compaction
// trigger flag propagation and the top-level property callbacks.
func isValidJSONDocument(raw []byte, callbacks *jsonDocumentCallbacks) bool {
	var stack compactJSONFrameStack
	index := skipJSONWhitespace(raw, 0)
	rootComplete := false
	var currentTopLevelKey string
	hasCurrentTopLevelKey := false
	type pendingProperty struct {
		key        string
		startIndex int
	}
	var pendingTopLevelProperty *pendingProperty

	consumeValue := func() bool {
		index = skipJSONWhitespace(raw, index)
		if index >= len(raw) {
			return false
		}
		byteValue := raw[index]
		if byteValue == jsonObjectOpenByte {
			index++
			stack.push(jsonObjectFirstKeyOrEndState)
			return true
		}
		if byteValue == jsonArrayOpenByte {
			index++
			stack.push(jsonArrayFirstValueOrEndState)
			return true
		}
		if byteValue == jsonStringByte {
			nextIndex, ok := skipValidJSONString(raw, index)
			if !ok {
				return false
			}
			index = nextIndex
			return true
		}
		if literalEnd, ok := validJSONLiteralEnd(raw, index); ok {
			index = literalEnd
			return true
		}
		numberEnd, ok := validJSONNumberEnd(raw, index)
		if !ok {
			return false
		}
		index = numberEnd
		return true
	}

	closeCurrentFrame := func() bool {
		frame, ok := stack.pop()
		if !ok {
			return false
		}
		if isJSONObjectFrame(frame) && (frame&jsonFrameTypeIsCompactionFlag) != 0 {
			callbacks.onCodexCompactionTrigger()
		}
		if pendingTopLevelProperty != nil && stack.length == 1 {
			callbacks.onTopLevelProperty(pendingTopLevelProperty.key, pendingTopLevelProperty.startIndex, index)
			pendingTopLevelProperty = nil
		}
		return true
	}

	if !consumeValue() {
		return false
	}
	if stack.length == 0 {
		rootComplete = true
	}

	for !rootComplete {
		index = skipJSONWhitespace(raw, index)
		frame, hasFrame := stack.peek()
		if !hasFrame {
			return false
		}
		state := jsonFrameState(frame)

		if isJSONObjectFrame(frame) {
			closeByte := byte(0)
			if index < len(raw) {
				closeByte = raw[index]
			}
			if state == jsonObjectFirstKeyOrEndState && closeByte == jsonObjectCloseByte {
				index++
				if !closeCurrentFrame() {
					return false
				}
			} else if state == jsonObjectFirstKeyOrEndState || state == jsonObjectKeyState {
				nextIndex, ok := skipValidJSONString(raw, index)
				if !ok {
					return false
				}
				key := decodeJSONInspectionKey(raw, index, nextIndex, stack.length == 1)
				index = nextIndex
				if stack.length == 1 {
					currentTopLevelKey = key
					hasCurrentTopLevelKey = true
				}
				nextFrame := jsonFrameWithState(frame, jsonObjectColonState) & ^byte(jsonFrameKeyIsTypeFlag)
				if key == "type" {
					nextFrame |= jsonFrameKeyIsTypeFlag
				}
				if !stack.replaceTop(nextFrame) {
					return false
				}
				continue
			} else if state == jsonObjectColonState {
				if closeByte != jsonColonByte {
					return false
				}
				index++
				if !stack.replaceTop(jsonFrameWithState(frame, jsonObjectValueState)) {
					return false
				}
				continue
			} else if state == jsonObjectValueState {
				var key string
				hasKey := false
				if stack.length == 1 {
					key = currentTopLevelKey
					hasKey = hasCurrentTopLevelKey
				} else if (frame & jsonFrameKeyIsTypeFlag) != 0 {
					key = "type"
					hasKey = true
				}
				valueStartIndex := skipJSONWhitespace(raw, index)
				nextFrame := jsonFrameWithState(frame, jsonObjectCommaOrEndState) & ^byte(jsonFrameKeyIsTypeFlag)
				if (frame & jsonFrameKeyIsTypeFlag) != 0 {
					if value, ok := readJSONStringToken(raw, index); ok && value.value == "compaction_trigger" {
						nextFrame |= jsonFrameTypeIsCompactionFlag
					} else {
						nextFrame &^= jsonFrameTypeIsCompactionFlag
					}
				}
				if stack.length == 1 {
					currentTopLevelKey = ""
					hasCurrentTopLevelKey = false
				}
				if !stack.replaceTop(nextFrame) {
					return false
				}
				depth := stack.length
				if !consumeValue() {
					return false
				}
				if depth == 1 && hasKey {
					if stack.length == depth {
						callbacks.onTopLevelProperty(key, valueStartIndex, index)
					} else {
						pendingTopLevelProperty = &pendingProperty{key: key, startIndex: valueStartIndex}
					}
				}
				continue
			} else if closeByte == jsonCommaByte {
				index++
				if !stack.replaceTop(jsonFrameWithState(frame, jsonObjectKeyState)) {
					return false
				}
				continue
			} else if closeByte == jsonObjectCloseByte {
				index++
				if !closeCurrentFrame() {
					return false
				}
			} else {
				return false
			}
		} else if state == jsonArrayFirstValueOrEndState && index < len(raw) && raw[index] == jsonArrayCloseByte {
			index++
			if !closeCurrentFrame() {
				return false
			}
		} else if state == jsonArrayFirstValueOrEndState || state == jsonArrayValueState {
			if !stack.replaceTop(jsonFrameWithState(frame, jsonArrayCommaOrEndState)) {
				return false
			}
			if !consumeValue() {
				return false
			}
			continue
		} else if index < len(raw) && raw[index] == jsonCommaByte {
			index++
			if !stack.replaceTop(jsonFrameWithState(frame, jsonArrayValueState)) {
				return false
			}
			continue
		} else if index < len(raw) && raw[index] == jsonArrayCloseByte {
			index++
			if !closeCurrentFrame() {
				return false
			}
		} else {
			return false
		}

		if stack.length == 0 {
			rootComplete = true
		}
	}

	return skipJSONWhitespace(raw, index) == len(raw)
}

func decodeJSONInspectionKey(raw []byte, index int, nextIndex int, topLevel bool) string {
	content := raw[index+1 : nextIndex-1]
	if bytes.IndexByte(content, jsonEscapeByte) < 0 {
		if bytes.Equal(content, jsonTypeKeyBuffer) {
			return "type"
		}
		if !topLevel {
			return ""
		}
		for _, candidate := range topLevelMetadataKeys {
			if bytes.Equal(content, candidate.buffer) {
				return candidate.key
			}
		}
		return ""
	}
	decoded, ok := decodeJSONStringToken(raw, index, nextIndex)
	if ok && decoded.value == "type" {
		return "type"
	}
	if ok && topLevel && topLevelMetadataKeySet[decoded.value] {
		return decoded.value
	}
	return ""
}

func skipValidJSONString(raw []byte, index int) (int, bool) {
	if index >= len(raw) || raw[index] != jsonStringByte {
		return 0, false
	}
	for cursor := index + 1; cursor < len(raw); cursor++ {
		byteValue := raw[cursor]
		if byteValue == jsonStringByte {
			return cursor + 1, true
		}
		if byteValue <= 0x1f {
			return 0, false
		}
		if byteValue != jsonEscapeByte {
			continue
		}
		cursor++
		if cursor >= len(raw) {
			return 0, false
		}
		escaped := raw[cursor]
		switch escaped {
		case jsonStringByte, jsonEscapeByte, jsonSlashByte, jsonLowerBByte,
			jsonLowerFByte, jsonLowerNByte, jsonLowerRByte, jsonLowerTByte:
			continue
		}
		if escaped != jsonLowerUByte || cursor+4 >= len(raw) {
			return 0, false
		}
		for offset := 1; offset <= 4; offset++ {
			if cursor+offset >= len(raw) || !isJSONHexByte(raw[cursor+offset]) {
				return 0, false
			}
		}
		cursor += 4
	}
	return 0, false
}

func validJSONLiteralEnd(raw []byte, index int) (int, bool) {
	if index+len(jsonNullBuffer) <= len(raw) && bytes.Equal(raw[index:index+len(jsonNullBuffer)], jsonNullBuffer) {
		return index + len(jsonNullBuffer), true
	}
	if index+len(jsonTrueBuffer) <= len(raw) && bytes.Equal(raw[index:index+len(jsonTrueBuffer)], jsonTrueBuffer) {
		return index + len(jsonTrueBuffer), true
	}
	if index+len(jsonFalseBuffer) <= len(raw) && bytes.Equal(raw[index:index+len(jsonFalseBuffer)], jsonFalseBuffer) {
		return index + len(jsonFalseBuffer), true
	}
	return 0, false
}

func validJSONNumberEnd(raw []byte, startIndex int) (int, bool) {
	index := startIndex
	at := func(i int) byte {
		if i < len(raw) {
			return raw[i]
		}
		return 0
	}
	if at(index) == jsonMinusByte {
		index++
	}
	if at(index) == jsonZeroByte {
		index++
		if isJSONDigitByte(at(index)) {
			return 0, false
		}
	} else if isJSONOneToNineDigitByte(at(index)) {
		index++
		for isJSONDigitByte(at(index)) {
			index++
		}
	} else {
		return 0, false
	}
	if at(index) == jsonDotByte {
		index++
		fractionStart := index
		for isJSONDigitByte(at(index)) {
			index++
		}
		if index == fractionStart {
			return 0, false
		}
	}
	if at(index) == jsonLowerEByte || at(index) == jsonUpperEByte {
		index++
		if at(index) == jsonPlusByte || at(index) == jsonMinusByte {
			index++
		}
		exponentStart := index
		for isJSONDigitByte(at(index)) {
			index++
		}
		if index == exponentStart {
			return 0, false
		}
	}
	return index, true
}

func isJSONHexByte(byteValue byte) bool {
	return isJSONDigitByte(byteValue) ||
		(byteValue >= 0x41 && byteValue <= 0x46) ||
		(byteValue >= 0x61 && byteValue <= 0x66)
}

func isJSONWhitespaceByte(byteValue byte) bool {
	return byteValue == 0x20 || byteValue == 0x0a || byteValue == 0x0d || byteValue == 0x09
}

func isValidJSONPrimitive(raw []byte, startIndex int, endIndex int) bool {
	start := skipJSONWhitespace(raw, startIndex)
	end := trimJSONWhitespaceEnd(raw, start, endIndex)
	return jsonLiteralEquals(raw, start, end, jsonNullBuffer) ||
		jsonLiteralEquals(raw, start, end, jsonTrueBuffer) ||
		jsonLiteralEquals(raw, start, end, jsonFalseBuffer) ||
		isValidJSONNumber(raw, start, end)
}

func trimJSONWhitespaceEnd(raw []byte, startIndex int, endIndex int) int {
	index := endIndex
	for index > startIndex && isJSONWhitespaceByte(raw[index-1]) {
		index--
	}
	return index
}

func jsonLiteralEquals(raw []byte, startIndex int, endIndex int, literal []byte) bool {
	return endIndex-startIndex == len(literal) && bytes.Equal(raw[startIndex:endIndex], literal)
}

func isValidJSONNumber(raw []byte, startIndex int, endIndex int) bool {
	index := startIndex
	if index >= endIndex {
		return false
	}
	if raw[index] == jsonMinusByte {
		index++
	}
	if index >= endIndex {
		return false
	}

	integerFirstByte := raw[index]
	if integerFirstByte == jsonZeroByte {
		index++
		if index < endIndex && isJSONDigitByte(raw[index]) {
			return false
		}
	} else if isJSONOneToNineDigitByte(integerFirstByte) {
		index++
		for index < endIndex && isJSONDigitByte(raw[index]) {
			index++
		}
	} else {
		return false
	}

	if index < endIndex && raw[index] == jsonDotByte {
		index++
		fractionStart := index
		for index < endIndex && isJSONDigitByte(raw[index]) {
			index++
		}
		if index == fractionStart {
			return false
		}
	}

	if index < endIndex {
		exponentByte := raw[index]
		if exponentByte == jsonLowerEByte || exponentByte == jsonUpperEByte {
			index++
			if index < endIndex && (raw[index] == jsonPlusByte || raw[index] == jsonMinusByte) {
				index++
			}
			exponentStart := index
			for index < endIndex && isJSONDigitByte(raw[index]) {
				index++
			}
			if index == exponentStart {
				return false
			}
		}
	}

	return index == endIndex
}

func isJSONDigitByte(byteValue byte) bool {
	return byteValue >= jsonZeroByte && byteValue <= jsonNineByte
}

func isJSONOneToNineDigitByte(byteValue byte) bool {
	return byteValue >= jsonOneByte && byteValue <= jsonNineByte
}

const (
	jsonStringByte      = 0x22
	jsonEscapeByte      = 0x5c
	jsonSlashByte       = 0x2f
	jsonLowerBByte      = 0x62
	jsonLowerFByte      = 0x66
	jsonLowerNByte      = 0x6e
	jsonLowerRByte      = 0x72
	jsonLowerTByte      = 0x74
	jsonLowerUByte      = 0x75
	jsonCommaByte       = 0x2c
	jsonColonByte       = 0x3a
	jsonMinusByte       = 0x2d
	jsonPlusByte        = 0x2b
	jsonDotByte         = 0x2e
	jsonZeroByte        = 0x30
	jsonOneByte         = 0x31
	jsonNineByte        = 0x39
	jsonLowerEByte      = 0x65
	jsonUpperEByte      = 0x45
	jsonObjectOpenByte  = 0x7b
	jsonObjectCloseByte = 0x7d
	jsonArrayOpenByte   = 0x5b
	jsonArrayCloseByte  = 0x5d
)

var (
	jsonTypeKeyBuffer = []byte("type")
)

type topLevelMetadataKey struct {
	key    string
	buffer []byte
}

var topLevelMetadataKeys = []topLevelMetadataKey{
	{"model", []byte("model")},
	{"service_tier", []byte("service_tier")},
	{"reasoning_effort", []byte("reasoning_effort")},
	{"reasoning", []byte("reasoning")},
	{"output_config", []byte("output_config")},
	{"generationConfig", []byte("generationConfig")},
	{"generation_config", []byte("generation_config")},
	{"stream", []byte("stream")},
	{"max_output_tokens", []byte("max_output_tokens")},
	{"max_tokens", []byte("max_tokens")},
	{"tools", []byte("tools")},
	{"tool_choice", []byte("tool_choice")},
	{"response_format", []byte("response_format")},
}

var topLevelMetadataKeySet = func() map[string]bool {
	set := make(map[string]bool, len(topLevelMetadataKeys))
	for _, key := range topLevelMetadataKeys {
		set[key.key] = true
	}
	return set
}()
