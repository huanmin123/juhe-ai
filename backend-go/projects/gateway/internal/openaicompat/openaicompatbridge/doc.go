// Package openaicompatbridge retains the native Gemini Code Assist unwrap of
// the openaicompat domain (REFACTOR-0008 阶段 A-2 遗存).
//
// 2026-11-07 跨协议 Responses↔Chat 桥退役：本包原 bridge request-body 构建
// 矩阵、response/SSE 转换矩阵、流式状态机、hosted-tool 执行面（code
// interpreter / computer adapter / hosted tool registry）与 dispatch 分发表
// 整体删除；openaicompatstorage 的 FileResolver / FileSearchExecutor 桥执行
// 端点实现（bridgeexecutors_impl.go）随端口一并删除。包内保留：
//
//   - Gemini Code Assist {response:...} envelope 解包（google_oauth
//     code_assist/google_one 原生路径，组合根
//     chain_bridge_response.go transformGeminiCodeAssistIfApplicable 消费）；
//   - aliases.go 的 endpoint family 常量与共享词汇转发（仍被
//     cmd/juhe-ai-gateway/chain_bridge_response.go 引用）。
package openaicompatbridge
