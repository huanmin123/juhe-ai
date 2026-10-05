package pricing

// geminiModelPricingData — 数据已迁移至 catalogdata/gemini.json（计划
// -20261005T230500000Z JSON 单一数据源）：改价改 JSON，本文件只保留加载
// 入口。原工厂（geminiTextModel/geminiTTSModel/geminiEmbeddingModel 与
// geminiTierPrices 派生）随迁移退役，历史裁决上下文见 git 历史与清洗指南。
var geminiModelPricingData = mustLoadProviderCatalogModels("gemini")
