# BUG-0368 语音转写 multipart 请求未提取 model 致入口 404

## 基本信息

- 编号：BUG-0368
- 状态：已修复并随 v2026.11.20 上线（2026-11-20）
- 严重程度：P1
- 发现时间：2026-11-20
- 发现方式：测试（真实上游协议矩阵）
- 模块：后端 / 网关 / 请求体中间件
- 关联计划：无
- 关联 bug：无
- 责任人：ZCode

## 问题概述

- 现象：`POST /v1/audio/transcriptions` 以 `multipart/form-data` 提交 `file` + `model` 时，网关在入口直接返回 404 `model_not_found`「请求缺少 model」，请求未到达上游。
- 期望：从 multipart 表单字段提取 `model`，按正常模型路由选择账户并转发上游。
- 实际：body 中间件只对图片 multipart 提取 `model`；音频 multipart 的 `req.State.Model` 恒空，调度按缺模型返回 404。
- 影响范围：全部使用 multipart 上传的 OpenAI 兼容语音转写/翻译请求（`/v1/audio/transcriptions`、`/v1/audio/translations`）；不影响 JSON 体请求与 TTS。

## 复现步骤

1. 在 dev 环境创建支持 `audio_transcription_json` 的音频账户并绑定分组。
2. 使用默认 API Key 发起 `POST /v1/audio/transcriptions`，body 为 `multipart/form-data`，字段 `file=@sample.mp3`、`model=volc.seedasr.auc`。
3. 观察网关响应为 404 `model_not_found`，日志无上游尝试（`attemptCount=0`）。

## 环境信息

- 分支 / 版本：master（2026-11-20 工作区）
- 数据状态：dev PostgreSQL schema revision 26
- 浏览器 / 系统 / Node 版本：Windows / Git Bash / Go 1.26
- 是否稳定复现：是

## 根因分析

- 表象：入口 404 报「请求缺少 model」。
- 真实根因：`gatewaybody` 的非 JSON body 分支只调用了 `ExtractMultipartImageMetadata`，没有音频 multipart 的 model 提取器。STT 的 `model` 与 `file` 同为 form-data 字段，不是 JSON 顶层字段，无法走 `ExtractJSONBodyMetadata`；`req.State.Model` 为空后，调度层按既有「请求缺少 model」契约返回 404。
- 为什么会发生：图片 multipart 的 model 提取在统一媒体计量批中已实现，音频同步面只补了 `response_format` 提取，遗漏了 `model`；单元测试也只覆盖图片 multipart model 与音频 `response_format`，没有覆盖音频 model。

## 修复方案

- 修改点：
  - `backend-go/projects/gateway/internal/gatewaybody/multipart.go` 新增 `ExtractMultipartAudioModel`：仅对 `/audio/transcriptions`、`/audio/translations`（含 `/v1` 前缀）的 multipart 请求生效，复用 busboy 语义的字段遍历与上限（16 字段 / 24 parts / 200 字节），字段重复、截断、空值或含 C0 控制字符时拒绝。
  - `backend-go/projects/gateway/internal/gatewaybody/middleware.go` 非 JSON 分支在图片 metadata 之后提取音频 model；`req.State.Model` 取值优先级为图片 model → 音频 model。
  - `multipart_test.go` 新增 `TestExtractMultipartAudioModel`；`middleware_test.go` 新增 multipart 音频 model 捕获用例。
- 行为影响：合法的 multipart STT 请求开始能进入调度与上游；非法/重复/超限 model 仍按缺模型处理，不放宽请求体边界。
- 发布异常处理：纯本地请求解析修复，无 schema、无配置、无上游协议变化；回滚为 binary-only。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 单元测试 | 音频 multipart model 提取与边界 | `go test ./internal/gatewaybody -run 'TestExtractMultipartAudioModel|TestExtractMultipartAudioResponseFormat|TestExtractMultipartImageModel|TestMiddleware' -count=1` | 全通过 | `ok ... 3.576s` | 通过 |
| 功能验证 | dev 真实请求进入上游 | `POST /v1/audio/transcriptions` multipart，model=`volc.seedasr.auc` | 不再本地 404，产生上游尝试 | 网关转发至 `openspeech.bytedance.com`，上游返回其自身 404（该同步端点缺口后由 BUG-0370 修复） | 通过（入口修复）；上游同步能力后已由 BUG-0370 补齐 |
| 回归验证 | 异步长转写 | `POST /v1/audio/jobs` + 轮询 + content | 200 全链 | 200，usage 4.493s / `costUsd=0.0001426349` | 通过 |

## 复发记录

- 时间：无
- 环境：无
- 现象：无
- 关联处理：无

## 下次遇到

- 先查什么：确认 `req.State.Model` 是否为空；检查请求 `Content-Type` 是否为 `multipart/form-data`。
- 重点看什么：新增 multipart 表单字段时，是否同时补了对应提取器与中间件接线；图片/音频提取器路径判定是否互斥。
- 如何避免误判：入口 404 且 `attemptCount=0` 是「请求体未解析出 model」的强信号，不要先怀疑上游或账户能力。

## 完成总结

- 完成时间：2026-11-20
- 结论：网关入口已修复并有单测锁定；真实上游同步 STT 的端点缺口后由 [BUG-0370](问题-0370-火山openspeech同步STT转发到不存在端点.md) 以 `recognize/flash` 同步 adapter 补齐，异步 `/v1/audio/jobs` 已全链通过。
- 后续建议：若产品要求火山同步 STT，需要新增对应同步 adapter 或从 `volcengine_openspeech` 词表移除 `audio_transcription_json`，二者择一并在设计文档中定稿。
