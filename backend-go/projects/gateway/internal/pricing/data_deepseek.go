package pricing

// DeepSeek pricing snapshot, ported from
// backend/src/modules/model-pricing/deepseek-model-pricing.data.ts (curated
// 2026-07-23). Since 2026-09-10 DeepSeek bills all models with official
// peak/off-peak time-of-day pricing (peak: weekdays UTC 01:00-04:00 and
// 06:00-10:00; weekends and Chinese public holidays are off-peak all day at
// half the peak rate). This snapshot is maintained at the user-confirmed
// peak-rate basis until time-of-day billing support lands; the fixed prices
// below therefore only match peak-window usage. Retired IDs
// (deepseek-v4-flash, retired 2026-09-10) are no longer kept here; the
// shutdown marker lives on the provider_model_catalog seed row only.
var deepSeekModelPricingData = mustLoadProviderCatalogModels("deepseek")
