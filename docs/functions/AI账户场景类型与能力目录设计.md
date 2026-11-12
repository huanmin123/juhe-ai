# AI 账户场景类型与能力目录设计

- 状态：实施中（2026-11-12 B1 底座已落地）
- 关联计划：[PLAN-20261111T154901000Z](../plans/计划-20261111T154901000Z-非对话账户内核统一与探针补齐.md)
- 复审记录：[deep-dive-review-非对话账户内核统一与探针补齐-20261112](../plans/deep-dive-review-非对话账户内核统一与探针补齐-20261112.md)
- 裁决：2026-11-12 用户 D13——不做向后兼容；非对话账户上线统一删除，只保留对话账户，删除后按新场景重建。

## 1. 目标

把“账户分类”和“菜单位置”解耦：

- 一个一级菜单「AI 账户」（管理面 `/accounts`、个人面 `/my-accounts`）；
- 创建账户时选「场景类型」；一个账户只属于一个场景；
- 列表按场景类型筛选，列展示场景类型；
- 以后新增场景只加注册表行，不再加菜单。

非目标：不重写 `/v1` 主链；不为每个场景复制账户系统；不做旧非对话账户迁移。

## 2. 场景类型

`backend-go/shared/platform/scenario` 是唯一注册表。当前已开放：

| 场景类型 | 名称 | 调度类 | 人工测试 | 生命周期（B1 现状） |
| --- | --- | --- | --- | --- |
| `chat` | 对话 | conversation | 模型 + 协议 | `pending_test_then_probe` |
| `video` | 视频 | stateless | connectivity | `active_only`（B4 改） |
| `audio` | 音频 | stateless | connectivity | `active_only`（B4 改） |
| `image` | 图像 | stateless | connectivity | `pending_test_then_probe` |

`embedding` / `rerank` / `moderation` / `realtime` 已在代码中保留类型常量，但未加入注册表；B7/B8 只追加 Profile 与 provider 矩阵，不改菜单/表单分支。

## 3. 数据与写侧

- `accounts.capability_domain` 沿用历史列名，语义为场景类型；D13 起**不再带四值 CHECK**，由写侧 `scenario.Valid` 校验。
- `capability_domain` 创建后不可原地修改；要换场景就新建账户。
- 写入口统一按场景校验，包括创建/PATCH/导入/克隆/公开 API/批量编辑/OAuth 托管建户/授权实例置备。
- `health_check_endpoint_mode` 仍保留 DB CHECK；B7 再增补 `rerank_json` / `moderation_json`。
- 上线 cutover：`maintenance purge-non-chat-accounts` 删除 `capability_domain <> 'chat'` 的账户（含授权实例、运行态、历史 usage/audit/stats）；不可逆，回滚只能恢复备份。旧二进制不再受支持。

## 4. 能力目录接口

```text
GET /__aisys__/api/accounts/capability-catalog          # 管理面，RequireAdmin
GET /__aisys__/api/my-accounts/capability-catalog       # 个人面，RequireSession
```

返回：

```json
{
  "scenarios": [
    {
      "scenarioType": "image",
      "label": "图像",
      "dispatchClass": "stateless",
      "providers": [
        {
          "providerCode": "xai",
          "profiles": [
            {
              "profileId": "profile_xai_openai_v1",
              "connectionPreset": "xai_openai_v1",
              "accountTypes": ["api_key"],
              "allowedEndpointModes": ["images_json"],
              "probeModes": ["images_json"],
              "testPolicy": "connectivity",
              "statusPolicy": "pending_test_then_probe"
            }
          ]
        }
      ]
    }
  ]
}
```

目录由 `contracts.AllowedConnectorCombos()` / `AllowedEndpointModeCombos()` + `scenario` 注册表派生，只读、不查库、不含凭据。**B1.3 已落地**：前端 `AccountsView` 打开时拉取管理面/个人面 `capability-catalog`，`accountCapabilityDomains.ts` 的账户类型与允许 modes 优先使用目录，目录不可用时才回退本地常量矩阵（测试/离线 fallback）；本地矩阵保留为兜底，不再作为正常运行时的第二事实源。

## 5. 前端

- `/accounts` 与 `/my-accounts` 是唯一路由；旧 `/accounts/{chat|video|audio|image}` 与 `/my-accounts/{...}` 不做兼容。
- 页面顶部「场景类型」筛选：全部 / 对话 / 视频 / 音频 / 图像；query `?scenario=<type>` 是唯一来源。
- 列表新增「场景」列；所有场景共用同一列集，不再按域隐藏优先级/时间计划。
- 创建弹窗当前沿用页面选中的场景；全部场景时默认 chat。B1.2 后续把场景选择移入创建向导第一步。

## 6. 验收

- 只有一个一级「AI 账户」菜单，管理面/个人面各一个入口；
- 创建时可选场景类型，列表可按场景筛选并显示场景列；
- 现有 chat/video/audio/image 账户全部可见可建；
- `capability_domain` 无 CHECK，gateway/jobs 启动门禁要求旧 CHECK 已删除；
- `purge-non-chat-accounts` dry-run/apply 门禁与 manifest 漂移拒绝可用；
- 旧非对话账户不迁移，按 D13 删除重建。
