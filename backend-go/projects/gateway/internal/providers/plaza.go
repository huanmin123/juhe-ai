// plaza.go owns the cross-vendor model plaza aggregate read (模型广场页与供应
// 商目录退役设计 §3)：GET /providers/models/plaza 把逐供应商目录（GET
// /{code}/models 的窗口与合并语义）跨全部 vendor 拼成一个响应。卡片跨供应商
// 平铺、同名模型跨供应商不合并（价格与归属各自独立）；写接口零变更。
package providers

import (
	"context"
	"net/http"
	"sort"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// PlazaProviderSummary mirrors the plaza `providers` entry (设计 §3.2)：窗口内
// 供应商摘要只暴露 code/name/enabled/defaultHealthCheckModel 四键，数据源即
// ListCatalogListItems（卡片据 enabled 灰显、据 defaultHealthCheckModel 挂
// 默认检查徽标），不透出 list 行的其余管理字段。
type PlazaProviderSummary struct {
	Code                    string `json:"code"`
	Name                    string `json:"name"`
	Enabled                 bool   `json:"enabled"`
	DefaultHealthCheckModel string `json:"defaultHealthCheckModel"`
}

// ListProviderModelPlaza aggregates the plaza items over the vendor window
// (设计 §3.2)：双臂（builtin ∪ global/personal）各一条 vendor_code IN (...)
// 单查询（无 per-vendor N+1），跨供应商不合并——每 vendor 内沿用
// ListProviderModelsForRequest 的合并、能力继承、isSupportedCatalogModel 过滤
// 与 release desc → catalogOrder → model 排序，再按 vendor 注册表顺序拼接。
// 广场窗口的 includeInactive/includeUnpriced 恒为 true（草稿行与无价行都返回，
// 状态徽标由前端区分）；includeCatalogHidden 只分流 catalogVisible=false 的
// builtin 行：管理面可见、用户面不可见（设计 §3.2，SQL 谓词不变，在聚合行上
// 施加，与 listItems 的管理/用户分流同模式）。custom 行无 catalog_visible 键，
// 不受该分流影响。
func (s *Store) ListProviderModelPlaza(ctx context.Context, vendorCodes []string, systemAccountID string, includeCatalogHidden bool) ([]ModelCatalogItem, error) {
	codes := normalizeProviderCodeList(vendorCodes)
	if len(codes) == 0 {
		return []ModelCatalogItem{}, nil
	}
	builtIn, err := s.listBuiltInCatalogModels(ctx, codes, true)
	if err != nil {
		return nil, err
	}
	custom, err := s.listCustomCatalogModels(ctx, codes, systemAccountID, true)
	if err != nil {
		return nil, err
	}
	// 按 vendor 分组后逐 vendor 走单供应商目录同一条管线；vendor 顺序即入参
	// 注册表顺序（ListCatalogListItems 的 name ASC, code ASC）。
	builtInByVendor := map[string][]ModelCatalogItem{}
	for _, item := range builtIn {
		builtInByVendor[item.ProviderCode] = append(builtInByVendor[item.ProviderCode], item)
	}
	customByVendor := map[string][]ModelCatalogItem{}
	for _, item := range custom {
		customByVendor[item.ProviderCode] = append(customByVendor[item.ProviderCode], item)
	}
	items := []ModelCatalogItem{}
	for _, code := range codes {
		vendorBuiltIn := builtInByVendor[code]
		merged := mergeModelCatalogItems(append(vendorBuiltIn, customByVendor[code]...))
		inheritCustomCatalogCapabilities(merged, vendorBuiltIn)
		filtered := []ModelCatalogItem{}
		for _, item := range merged {
			if !isSupportedCatalogModel(item) {
				continue
			}
			if !includeCatalogHidden && item.CatalogVisible != nil && !*item.CatalogVisible {
				continue
			}
			filtered = append(filtered, item)
		}
		sort.SliceStable(filtered, func(left, right int) bool {
			return compareProviderModelCatalogItems(filtered[left], filtered[right]) < 0
		})
		// withCatalogDisplay rides on every merged catalog row (Node maps the
		// sorted catalog through withCatalogDisplay)。
		for index := range filtered {
			display := buildProviderCatalogDisplay(&filtered[index])
			filtered[index].CatalogDisplay = &display
		}
		items = append(items, filtered...)
	}
	return items, nil
}

// providerModelPlaza serves GET /providers/models/plaza (设计 §3)：read 中间
// 件族；viewScope=admin（isManagementProviderRequest）扩窗口到 disabled
// vendor 与 catalogVisible=false 的 builtin 行，systemAccountId 复用
// requestSystemAccountID（admin 可传/all 视空，非 admin pin 自己）。
// includeInactive/includeUnpriced 不设参数（现弹窗两参恒 true，广场保持差异=0）。
func (d *Deps) providerModelPlaza(w http.ResponseWriter, r *http.Request) {
	management := isManagementProviderRequest(r)
	list, err := d.Store.ListCatalogListItems(r.Context())
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "服务器内部错误", err)
		return
	}
	// 窗口分流与 listItems 同语义：管理面全量 vendors（disabled 供应商灰显），
	// 用户面仅 enabled 供应商。
	window := make([]ProviderListItem, 0, len(list))
	for _, item := range list {
		if management || item.Enabled {
			window = append(window, item)
		}
	}
	codes := make([]string, 0, len(window))
	for _, item := range window {
		codes = append(codes, item.Code)
	}
	items, err := d.Store.ListProviderModelPlaza(r.Context(), codes, requestSystemAccountID(r), management)
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "服务器内部错误", err)
		return
	}
	providers := make([]PlazaProviderSummary, 0, len(window))
	for _, item := range window {
		providers = append(providers, PlazaProviderSummary{
			Code:                    item.Code,
			Name:                    item.Name,
			Enabled:                 item.Enabled,
			DefaultHealthCheckModel: item.DefaultHealthCheckModel,
		})
	}
	kernel.WriteOK(w, map[string]any{"providers": providers, "items": items}, "")
}
