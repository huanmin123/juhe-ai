// custom_capability_inherit.go owns the custom catalog-row capability
// inheritance contract (BUG-0229, 文档契约见 docs/functions/AI问答设计.md
// 8.6「custom 目录行能力继承」、AI问答工具体系与主子模型设计 6.4): a custom
// (global/personal) catalog row that overrides a built-in row under the catalog
// merge key inherits the capability keys from that built-in row (after the
// built-in static-derived fallback), filling only empty keys. The chat face
// (cmd/juhe-ai-gateway/chain_catalog.go) and the admin face (catalog.go
// listProviderModelCatalog) both resolve through
// InheritBuiltinCatalogCapabilities so the two faces cannot drift — the same
// parity principle BUG-0210 established for the static fallback itself.
package providers

// CustomCatalogCapabilityKeys projects the capability keys a catalog row
// carries into the catalog payloads (supportedToolsByProtocol /
// inputModalities / outputModalities). custom_provider_models has no such
// columns, so custom rows enter with empty keys; built-in rows arrive already
// passed through the static-derived fallback
// (ApplyBuiltInStaticDerivedFields / decorateBuiltinStaticDerivedCapabilities).
// SupportedToolsByProtocol 是「协议 × 工具」矩阵；目录投影的一维
// supportedTools 过渡字段由调用侧以二维并集派生（阶段 2 随 chat 面切换删除）。
type CustomCatalogCapabilityKeys struct {
	SupportedToolsByProtocol map[string][]string
	InputModalities          []string
	OutputModalities         []string
}

// InheritBuiltinCatalogCapabilities 实现「仅填空」的二维矩阵继承：custom 行的
// 空能力键以被覆盖内置行（经静态兜底后）的值填充，非空键不覆盖（矩阵整体为
// 空才继承，键级合并语义与一维时代一致——custom 行没有能力列，实际只会整体
// 继承或不继承）；内置无对应行（全新自定义模型）保持空。不得退回静态定价表的
// 别名/前缀匹配回填（pricing.lookup 的日期后缀剥离与前缀别名会对
// "gpt-5.5-my" 这类自有命名误配能力）。返回的矩阵可能直接引用 builtin 的底层
// 数据：目录项下游是只读投影，不做变更。
func InheritBuiltinCatalogCapabilities(custom, builtin CustomCatalogCapabilityKeys) CustomCatalogCapabilityKeys {
	if len(custom.SupportedToolsByProtocol) == 0 {
		custom.SupportedToolsByProtocol = builtin.SupportedToolsByProtocol
	}
	if len(custom.InputModalities) == 0 {
		custom.InputModalities = builtin.InputModalities
	}
	if len(custom.OutputModalities) == 0 {
		custom.OutputModalities = builtin.OutputModalities
	}
	return custom
}
