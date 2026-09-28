// custom_capability_inherit.go owns the custom catalog-row capability
// inheritance contract (BUG-0226, 文档契约见 docs/functions/AI问答设计.md
// 8.6「custom 目录行能力继承」): a custom (global/personal) catalog row that
// overrides a built-in row under the catalog merge key inherits the three
// capability keys from that built-in row (after the built-in static-derived
// fallback), filling only empty keys. The chat face
// (cmd/juhe-ai-gateway/chain_catalog.go) and the admin face (catalog.go
// listProviderModelCatalog) both resolve through
// InheritBuiltinCatalogCapabilities so the two faces cannot drift — the same
// parity principle BUG-0210 established for the static fallback itself.
package providers

// CustomCatalogCapabilityKeys projects the three capability keys a catalog
// row carries into the catalog payloads (supportedTools / inputModalities /
// outputModalities). custom_provider_models has no such columns, so custom
// rows enter with empty keys; built-in rows arrive already passed through
// the static-derived fallback (ApplyBuiltInStaticDerivedFields /
// decorateBuiltinStaticDerivedCapabilities).
type CustomCatalogCapabilityKeys struct {
	SupportedTools   []string
	InputModalities  []string
	OutputModalities []string
}

// InheritBuiltinCatalogCapabilities 实现「仅填空」继承：custom 行的空能力键
// 以被覆盖内置行（经静态兜底后）的值填充，非空键不覆盖；内置无对应行（全新
// 自定义模型）保持空。不得退回静态定价表的别名/前缀匹配回填（pricing.lookup
// 的日期后缀剥离与前缀别名会对 "gpt-5.5-my" 这类自有命名误配能力）。
// 返回的切片可能直接引用 builtin 的底层数组：目录项下游是只读投影，不做变更。
func InheritBuiltinCatalogCapabilities(custom, builtin CustomCatalogCapabilityKeys) CustomCatalogCapabilityKeys {
	if len(custom.SupportedTools) == 0 {
		custom.SupportedTools = builtin.SupportedTools
	}
	if len(custom.InputModalities) == 0 {
		custom.InputModalities = builtin.InputModalities
	}
	if len(custom.OutputModalities) == 0 {
		custom.OutputModalities = builtin.OutputModalities
	}
	return custom
}
