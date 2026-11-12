// qwen_image.go 是通义百炼 Qwen-Image 同步生图 adapter（2026-11-11 同步图像
// 批，契约 §10.3）。对外 OpenAI Images；上游为 DashScope 原生
// multimodal-generation/generation：请求 input.messages 文本 + parameters，
// 响应 output.choices[].message.content[].image；2.0 与 3.0 的 usage 字段不同。
package gatewaymedia

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type qwenImageAdapter struct{}

func (qwenImageAdapter) Provider() string { return "qwen" }

func (qwenImageAdapter) Build(input ImageRequest) (ImageBuildResult, error) {
	body := deepCopyMap(input.Body)
	opts, _, err := takeImageProviderOptions(body)
	if err != nil {
		return ImageBuildResult{}, err
	}
	sub := map[string]any{}
	if len(opts) > 0 {
		for key, value := range opts {
			if providerOptionKeyMatches("qwen", key) {
				sub = value
				break
			}
		}
	}

	model, _ := body["model"].(string)
	model = strings.TrimSpace(model)
	if model == "" {
		return ImageBuildResult{}, fmt.Errorf("图像生成请求缺少 model")
	}
	prompt, _ := body["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		return ImageBuildResult{}, fmt.Errorf("图像生成请求缺少 prompt")
	}

	applied := make([]string, 0, 4)
	sizeIgnored := false
	content := []any{map[string]any{"text": prompt}}
	if image, ok := body["image"].(string); ok && strings.TrimSpace(image) != "" {
		content = append(content, map[string]any{"image": image})
		applied = append(applied, "image")
	}

	parameters := map[string]any{}
	if size, ok := body["size"].(string); ok {
		switch normalized := strings.TrimSpace(size); {
		case normalized == "":
		case strings.EqualFold(normalized, "auto"):
			// qwen 无 auto 语义，省略由上游默认；回显 params_ignored.size。
			sizeIgnored = true
		default:
			parameters["size"] = strings.ReplaceAll(strings.ReplaceAll(normalized, "x", "*"), "X", "*")
			applied = append(applied, "size")
		}
	}
	if n, ok := body["n"]; ok {
		parameters["n"] = n
		applied = append(applied, "n")
	}

	providerOptionKeys := sortedStringKeys(sub)
	if nested, ok := sub["parameters"].(map[string]any); ok {
		mergeProviderValues(parameters, nested)
	}
	rest := make(map[string]any, len(sub))
	for key, value := range sub {
		if key == "parameters" {
			continue
		}
		rest[key] = value
	}
	if image, ok := rest["image"]; ok {
		delete(rest, "image")
		if imageText, ok := image.(string); ok && strings.TrimSpace(imageText) != "" {
			hasImage := false
			for _, item := range content {
				if contentItem, ok := item.(map[string]any); ok {
					if _, has := contentItem["image"]; has {
						hasImage = true
						break
					}
				}
			}
			if !hasImage {
				content = append(content, map[string]any{"image": imageText})
				applied = append(applied, "image")
			}
		}
	}
	mergeProviderValues(parameters, rest)
	ignored := dropImageParams(parameters, "stream", "partial_images",
		"quality", "background", "output_format", "output_compression",
		"safety_identifier", "response_format", "moderation", "user")
	ignored = append(ignored, dropImageParams(body, "quality", "background", "output_format",
		"output_compression", "stream", "partial_images", "safety_identifier",
		"response_format", "moderation", "user")...)
	if sizeIgnored {
		ignored = append(ignored, "size")
	}
	ignored = sortedUniqueStrings(ignored)
	providerOptionKeys = excludeStrings(providerOptionKeys, ignored)
	if _, ok := parameters["watermark"]; !ok {
		parameters["watermark"] = false
	}
	applied = append(applied, "watermark")

	native := map[string]any{
		"model": model,
		"input": map[string]any{
			"messages": []any{
				map[string]any{"role": "user", "content": content},
			},
		},
		"parameters": parameters,
	}
	encoded, err := json.Marshal(native)
	if err != nil {
		return ImageBuildResult{}, fmt.Errorf("qwen 图像请求体序列化失败: %w", err)
	}
	return ImageBuildResult{
		Body:                   encoded,
		Provider:               "qwen",
		ParamsApplied:          sortedUniqueStrings(applied),
		ParamsIgnored:          ignored,
		ProviderOptionsApplied: providerOptionKeys,
	}, nil
}

func (qwenImageAdapter) UpstreamPath(string) string { return qwenImageUpstreamPath }
func (qwenImageAdapter) RequiresResponseTransform() bool {
	return true
}

// TransformResponse 把 DashScope 原生响应转为 OpenAI Images：
// output.choices[].message.content[].image → data[].url；
// usage 2.0 image_count / 3.0 output_image_count → output_image_count。
func (qwenImageAdapter) TransformResponse(_ ImageRequest, body []byte) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("qwen 图像响应不是有效 JSON: %w", err)
	}
	images := qwenImageURLs(root)
	if len(images) == 0 {
		return nil, fmt.Errorf("qwen 图像响应没有可用图片")
	}
	data := make([]any, 0, len(images))
	for _, image := range images {
		data = append(data, map[string]any{"url": image})
	}
	created := time.Now().Unix()
	if value, ok := imageIntValue(root["created"]); ok {
		created = int64(value)
	}
	out := map[string]any{
		"created": created,
		"data":    data,
		"usage": map[string]any{
			"output_image_count": len(images),
		},
	}
	if model, ok := root["model"].(string); ok && strings.TrimSpace(model) != "" {
		out["model"] = model
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("qwen 图像响应序列化失败: %w", err)
	}
	return encoded, nil
}

func qwenImageURLs(root map[string]any) []string {
	output, _ := root["output"].(map[string]any)
	choices, _ := output["choices"].([]any)
	images := make([]string, 0, len(choices))
	for _, rawChoice := range choices {
		choice, _ := rawChoice.(map[string]any)
		message, _ := choice["message"].(map[string]any)
		content, _ := message["content"].([]any)
		for _, rawItem := range content {
			item, _ := rawItem.(map[string]any)
			for _, key := range []string{"image", "image_url"} {
				if image, ok := item[key].(string); ok && strings.TrimSpace(image) != "" {
					images = append(images, image)
				}
			}
		}
	}
	return images
}

func imageIntValue(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), typed >= 0
	case int:
		return typed, typed >= 0
	case int64:
		return int(typed), typed >= 0
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil && parsed >= 0
	case string:
		var parsed int
		if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%d", &parsed); err == nil && parsed >= 0 {
			return parsed, true
		}
	}
	return 0, false
}
