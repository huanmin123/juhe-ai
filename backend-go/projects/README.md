# Go 项目骨架

这里是三个可独立构建和部署的 Go 项目模块：

- `gateway`：对外 API、管理 API 和 AI 上游桥接。
- `jobs`：定时任务、探活、复制、统计和周期维护。
- `maintenance`：一次性 schema、迁移、回填、重建和诊断命令。

`../shared/contracts` 是稳定无业务契约库，`../shared/platform` 是无业务编排的基础设施库；二者都不是部署项目。三个项目之间禁止直接 import。`gateway` 当前承载 F3/F4，`jobs` 当前承载 F1/F2，`maintenance` 仅提供一次性命令骨架。

后续迁移顺序：先在 `jobs` 中实现一个完整定时功能及其 Store/lease/观测，再切断对应 Node owner；不要把新任务加回 `gateway`，也不要保留长期双写。
