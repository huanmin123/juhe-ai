package pricing

// anthropicModelPricingData — curated from Anthropic's official docs.
// Official deprecations (checked 2026-09-20) show the current dated rows as
// Active with tentative floors only ("not sooner than"), no confirmed
// shutdown dates, so no row carries ShutdownDate here.
var anthropicModelPricingData = mustLoadProviderCatalogModels("anthropic")
