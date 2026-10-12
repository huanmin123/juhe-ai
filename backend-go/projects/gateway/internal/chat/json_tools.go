package chat

// 通用 JSON 工具（压缩摘要解析消费的纯函数；图像结果剥离扫描器与其余
// responses 解析助手已随生图工具/Responses 旧解析分支退场）。

func objectItem(value any) map[string]any {
	if item, ok := value.(map[string]any); ok {
		return item
	}
	return map[string]any{}
}
