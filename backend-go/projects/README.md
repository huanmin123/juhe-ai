# Go 项目骨架

这里是三个可独立构建和部署的 Go 项目模块：

- `gateway`：对外 API、管理 API 和 AI 上游桥接。
- `jobs`：定时任务、探活、复制、统计和周期维护。
- `maintenance`：一次性 schema、迁移、回填、重建和诊断命令。

`../shared/contracts` 是稳定无业务契约库，`../shared/platform` 是无业务编排的基础设施库（包括统一上游传输 `upstreamhttp` 和 SQL pool 生命周期 `sqlpool`）；二者都不是部署项目。三个项目之间禁止直接 import。`gateway` 当前承载 F3/F4；方案 A 的 J3b 也只能由 gateway 同进程接管，`jobs` 必须拒绝启用 J3b。`jobs` 当前承载 F1/F2，`maintenance` 仅提供一次性命令骨架。

后续迁移顺序：一般定时功能先在 `jobs` 中实现完整 Store/lease/观测，再切断对应 Node owner；方案 A 的 J3b 是 gateway 例外，具体边界以 `docs/migration/J3b-模型检测完整迁移契约.md` 为准。任何功能都不得保留长期双写。
