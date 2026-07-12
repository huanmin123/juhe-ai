# BUG-0070 AI 问答原生图片绕过 HTTPS 限制

## 基本信息

- 状态：已修复（开发验证）
- 严重程度：P2
- 模块：前端 / AI 问答 / Markdown / 安全
- 发现时间：2026-07-12

## 现象与触发条件

Markdown 图片语法会拒绝非 HTTPS 地址，但消息直接包含 `<img src=x>` 时，`marked` 会保留原生 HTML，DOMPurify 只移除事件属性，不会主动删除该图片节点。

## 根因

URL 策略只写在 `marked` 的图片 renderer 中，没有覆盖 Markdown 原生 HTML。DOMPurify 负责 XSS 清洗，不等价于产品层的 HTTPS-only 资源策略。

## 修复

在最终消毒前统一遍历全部 `img`，只保留 `https://`，其余节点替换为安全替代文本；公式渲染同时改为只处理普通文本节点，跳过 `code`、`pre` 和链接。

## 验证

浏览器输入原生图片、事件属性、`javascript:` 链接、HTTP 图片和代码块公式，确认脚本执行、事件属性、危险链接、非 HTTPS 图片和代码内 KaTeX 节点计数均为 0。

## 防复发

富文本安全测试必须同时覆盖 Markdown renderer 语法和原生 HTML 两条入口；DOMPurify 通过不代表业务 URL policy 已通过。

## 关联

- 计划：`PLAN-0092`
- 关联 bug：无
