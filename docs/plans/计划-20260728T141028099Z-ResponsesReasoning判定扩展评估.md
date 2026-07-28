# PLAN-20260728T141028099Z Responses Reasoning 判定扩展评估

## 状态

已关闭，不实施。

## 评估范围

- 在 Codex Responses 运行时识别 `<thinking>`、`reasoning unavailable`、reasoning 不可见或疑似不完整。
- 在严格拦截模式下根据上述信号排除账户并换号。
- 在“模型检测”菜单中把 reasoning 结构或正文异常作为假模型证据。

## 结论

不新增正文或 reasoning 语义启发式判定。现有硬协议防火墙继续保留，但弱内容信号不进入自动拦截、切号、账户处罚或假模型定性。

## 关闭原因

- `<thinking>` 位于 `output_text` 时属于合法正文，无法和用户要求的 XML、代码、日志或解释文本可靠区分。
- Responses 合法允许没有可读 reasoning，summary 为空或只有 encrypted content。
- OpenAI/Codex 没有提供隐藏 reasoning 的预期长度、校验和或语义完成标志。
- 结构异常只能证明服务链路不符合目标契约，不能证明底层物理模型为假。
- 弱信号误判会导致合法请求中断；提交后换号还会造成跨账户输出混合。
- 模型检测探针可能被上游针对性适配，单次或少量样本不能形成确定身份结论。

## 保留边界

- 确定的 Responses envelope、item contract、ID、事件阶段和 provenance 检查不撤销。
- 缺少协议终态、明确 `response.failed` 和传输中断继续按现有失败路径处理。
- 模型检测继续展示协议能力、响应模型字段、工具调用、结构化输出、usage、行为与稳定性结果，但不新增 reasoning 正文真假判定。

## 重新评估条件

- OpenAI 发布可机器验证的 reasoning 完成契约；或
- 获得多来源、按模型/effort/任务分层的标注样本，并证明阈值在真实环境中具有可接受的误报率；或
- 出现不依赖正文语义、可由原始 wire facts 确定复现的新协议证据。

本计划没有修改运行时代码、账户配置、数据库或前端。
