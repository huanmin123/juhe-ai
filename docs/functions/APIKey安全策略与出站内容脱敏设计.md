# API Key 安全策略与出站内容脱敏设计

> 状态：**已实施（2026-11-07，批A-F 全量落地：schema revision 19、实体与绑定、请求侧脱敏+注入、响应侧流式还原、usage 两列、前端页面）；待发布与真机验收。** 批次交付注记：批A 存储面与实体 CRUD（`internal/securitypolicies`、`internal/apikeys` 绑定、`internal/gatewaysecpol` 核心）、批B2 运行链接入（`chain_security_policy.go` 单点出站脱敏、编译缓存 `gatewayruntimecache`、usage 请求事实透传）、批C 响应侧占位符还原（`gatewayresponse` 统一响应管道字节面挂载，见 §7.4 实施注记；顺带修复 gatewayresponse ParseError 隔离器跨 push 持有字节丢失缺陷）；jobs `usagewriter` 落列镜像、媒体域接入（`chain_security_policy.go` 按 `gatewaymedia.RequestLaneForPath` 词表统一映射 `gatewaysecpol.FamilyMedia`）与前端页面（策略双页面 + API Key 表单/列表 + usage 命中徽标）均已随批次落地。接口契约权威表述见 [接口契约与权限矩阵](接口契约与权限矩阵.md)「安全策略」章。2026-11-07 同日增补：**防护次数（写后缓存，§4.3.1）**——请求终态 INCRBY Redis + gateway 5 分钟 GETDEL 合并 `security_policies.protection_count`（revision 21 纯 DDL 空号）+ 读侧 PG 列投影 + 前端「防护次数」列，已随批实施、随下一发布窗口真机验收（`docs/待办与阻塞事项.md` #T180）。
> 一期范围（2026-11-06 用户裁决）：动作只做 **mask + placeholder**（不做 block）；扫描范围 = **凡可能把用户内容带给上游的字段都扫**（对话文本 + 工具调用参数）；规则归属 = **策略实体，API Key 绑定**（同调度偏好范式）。
> 关联：[接口契约与权限矩阵](接口契约与权限矩阵.md)（安全策略章）、[安全与日志策略](安全与日志策略.md)、[请求处理分层设计](请求处理分层设计.md)、[响应语义检查管线设计](响应语义检查管线设计.md)、[内容指纹会话身份设计](内容指纹会话身份设计.md)、[计划-20261012T033000000Z-调度偏好独立实体与APIKey绑定](../plans/计划-20261012T033000000Z-调度偏好独立实体与APIKey绑定.md)（实体/绑定/菜单范式）。

## 1. 问题与目标

部署形态是中间层：客户端 → 本网关 → 上游厂商（gpt / anthropic / gemini / deepseek / glm 等）。客户担心手机号、身份证号、银行卡号、邮箱、内网地址、密钥等敏感信息随请求原文到达上游厂商（被留存、训练或泄露）。

本设计在**请求出站方向**加一道内容处理：命中规则的敏感片段，按动作替换后再发给上游；上游响应回来后，把占位符还原成真实值再返回客户端。**脱敏映射表只存在于网关进程内、单次请求生命周期内，不落盘、不进 Redis、不发往上游**；请求原文（含真实值）仍按现行审计与日志策略持久化（§8，现行网关行为，不由本设计改变）——「上游拿不到真实值」是本设计的承诺，「网关不留存请求原文」不是，交付文案不得混用这两句话。

### 1.1 目标

1. 新增独立实体「安全策略」：管理面 / 个人面菜单各一，规则集配置在策略上，API Key 绑定策略（五写入口矩阵同调度偏好）。
2. 一期动作两种：
   - `mask`：本地打码（如 `138****5678`），不可逆，发给上游的就是打码值；
   - `placeholder`：伪名化，发给上游 `[[PHONE_1]]`，响应回来还原成真实值。
3. 扫描对象：请求侧所有承载用户内容给上游的文本与工具参数，含媒体域（视频/音频/图像生成）的 prompt 类文本字段（§5 字段清单）。
4. 未绑定 = 内置「默认安全策略」（规则集为空）= 请求原样转发，现有行为逐字节不变。
5. 每次请求记录命中计数（按规则、无原文），进使用记录。
6. 策略含 `placeholder` 规则时自动注入固定出站系统提示（§6），使模型把占位符当作 opaque 值保真处理：不改写、不补全、需要引用时原样输出、被问及真实值时说明已遮蔽。

### 1.2 非目标

- 不做 `block` 动作（二期候选）。
- 不做上游数据政策标签 / 路由约束（独立立项）。
- 不改日志与审计的捕获、保留、脱敏规范：本功能不触碰日志层；审计链按 §8 如实记录"客户端原文 / 发往上游的脱敏后内容"。
- 不做 NER / 本地模型检测：一期只做确定性规则（正则 + 关键词 + 校验算法）。
- 不承诺拦截所有泄漏：规则是概率防线，文档明示边界（§9）。

## 2. 总体流程与映射表生命周期

```text
客户端请求
  → 现有入口链（认证 / 预检 / body 解析与保护）          [不变]
  → 出站脱敏（绑定了非默认策略的 Key 才执行）：
      文本节点遍历 → 规则匹配 → 替换
      产出：sanitized body + 映射表（per request，进程内存）
      含 placeholder 规则的策略同时注入固定系统提示（§6）
  → 内容指纹（保持现状：preflight 阶段按原始 body 计算，不消费 sanitized）
  → 调度、上游准备、协议桥接、重试
      （全部消费 sanitized body；同一请求所有上游尝试共用同一映射）
  → 审计：client_request 记客户端原文（现行策略照存），
      upstream_request 记实际发送的 sanitized body
  → 上游响应
  → 响应语义检查 / inspector（不变）
  → 占位符还原（流式跨 chunk 缓冲）→ 写给客户端
  → 请求终态：映射表销毁
```

- **脱敏执行一次，在入口 body 定型后**：多账户重试、协议桥接（chat→responses、messages→chat 等）都从 sanitized body 派生，占位符全链路一致。
- **映射表**：请求级数据结构，随请求上下文传播，终态销毁。不落库、不进 spool、不进 Redis、不写日志。
- **编号稳定性**：占位符编号 = 同标签在请求内首次出现顺序从 1 递增；同一原文片段在同请求内复用同一编号（同一手机号全文只占一个编号）。多轮会话客户端重发历史时，历史文本相同 → 编号相同 → 上游 prompt cache 命中不受影响。
- **前缀缓存保持契约（硬性）**：替换必须是确定性的纯函数——同一原文在同一策略版本下永远得到同一替换结果，且遍历顺序确定。满足此契约时，脱敏不破坏上游前缀缓存：多轮会话的历史前缀字节一致，KV cache 照常命中；上游 usage 的 `cache_read_tokens` 可直接观测验证。破坏确定性的实现（随机占位符成分、无序遍历）一律不允许。已知的一次性 miss 属预期：策略首次绑定到 Key、策略规则修改；均只影响变更后的第一轮，之后恢复命中。

## 3. 规则模型

### 3.1 规则集结构（`rules_json`）

```json
{
  "rules": [
    {
      "id": "r1",
      "label": "PHONE",
      "type": "phone",
      "action": "placeholder",
      "maskKeep": 3
    },
    {
      "id": "r2",
      "label": "PROJECT_CODE",
      "type": "custom_keywords",
      "keywords": ["XX项目", "XX-2026"],
      "action": "mask"
    },
    {
      "id": "r3",
      "label": "EMPLOYEE_ID",
      "type": "custom_regex",
      "pattern": "GH-[0-9]{6}",
      "action": "placeholder"
    }
  ]
}
```

### 3.2 内置规则类型（pattern 由代码提供，不可改写）

| type | 匹配目标 | 误报防线 | mask 默认 |
| --- | --- | --- | --- |
| `phone` | 大陆手机号 | 前后非数字边界断言 | 保留前 3 后 4：`138****5678` |
| `id_card` | 18 位身份证 | GB11643 校验位验证 | 保留前 6 后 4 |
| `bank_card` | 13-19 位银行卡 | Luhn 校验 | 保留前 4 后 4 |
| `email` | 邮箱 | 常见 RFC 简化形态 | 首字符 + `***` + `@域名` |
| `ipv4` | IPv4 地址 | 八段 0-255 校验 | `10.*.*.*` 保留首段 |
| `secret` | 密钥形态（`sk-`、`Bearer `、`AKIA`、≥24 位连续混合 token 等） | 长度与字符集门槛 | 保留前 4 后 4，中段 `****` |

误报提示（随规则交付文案明示）：`ipv4` 会命中版本号与点分数字（如 `1.2.3.4`）；`email` 会命中 `@提及` 与代码中的地址形态。两类规则的误报面由客户按业务自行权衡是否启用与选择动作。

### 3.3 自定义规则

- `custom_regex`：RE2 语法，长度上限 256 字节；编译失败 / 超限 → 创建策略时 400。
- `custom_keywords`：字面匹配，最多 100 个，单个 ≤ 64 字节；大小写敏感。
- `maskKeep`：mask 动作可选拒绝保留端（默认按 type；自定义规则默认整体 `****`，保留长度 0）。
- `label`：placeholder 标签，`[A-Z][A-Z0-9_]{0,30}`，默认按 type（PHONE / ID_CARD / BANK_CARD / EMAIL / IPV4 / SECRET / CUSTOM）。

### 3.4 占位符格式

- 形态：`[[LABEL_N]]`（双方括号降低与正文冲突概率）。
- 同标签按首次出现顺序编号：第一个手机号 → `[[PHONE_1]]`，第二个 → `[[PHONE_2]]`；同一原文片段在同请求内复用同一编号。
- 还原只匹配**本次映射表中存在的编号**（白名单匹配）：正文中恰好出现的 `[[PHONE_99]]` 若不在表内，不还原、原样透传。

### 3.5 校验约束

- 每策略规则数 ≤ 50；`rules_json` 总长 ≤ 64KB。
- 规则按数组顺序执行，先命中先替换；一条文本可被多条规则依次命中（后者匹配前者替换后的文本）。

## 4. 实体、绑定与菜单（沿用调度偏好范式）

### 4.1 数据模型（纯加法）

```sql
CREATE TABLE IF NOT EXISTS security_policies (
  id text PRIMARY KEY,
  system_account_id text,
  name text NOT NULL,
  description text,
  rules_json text,
  built_in integer NOT NULL DEFAULT 0 CHECK (built_in IN (0, 1)),
  created_at text NOT NULL,
  updated_at text NOT NULL
);
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS security_policy_id text
  REFERENCES security_policies(id);
```

索引、owner 唯一约束、内置行固定 ID 唯一约束与调度偏好同构。

- 内置行：`id='secpol-builtin-default'`、`name='默认安全策略'`、`rules_json='{"rules":[]}'`（空规则 = 不处理 = 现有行为）、`system_account_id=NULL`、`built_in=1`，seed 幂等 upsert，业务写入口 403。
- `usage_records`（juhe_usage 日分区）加两列：`security_policy_applied integer`（0/1）、`pii_hits_json text`（按规则计数，无原文）；走既有加列机制（PG ensure + shard PRAGMA 守卫）。

### 4.2 API Key 绑定（五写入口矩阵，字段 `securityPolicyId`）

与调度偏好矩阵逐行同构：创建缺省/null → 内置 ID；PATCH 缺失 → 保持原值；null → 内置 ID；非空 ID → 同 owner 自定义或内置行，否则统一 `400 { message: "安全策略不存在或不可用" }`；公开面 `api-key/add|update` 同规则；绑定变更计入 revision CAS 与 `changedFields`，提交后执行既有 `InvalidateValidation`。

列表/详情/公开摘要新增 `securityPolicyId`、`securityPolicyName`、`securityPolicyBuiltIn`。

### 4.3 策略 CRUD

- 双面 12 端点：`/__aisys__/api/security-policies`（RequireAdmin）与 `/__aisys__/api/my-security-policies`（RequireSession + selfScope），路径、scope、`expectedUpdatedAt` CAS、同值 no-op、删除同事务解绑回内置并返回 `resetBindings`、操作日志 `security_policies.create|update|delete`，全部对齐调度偏好契约。
- PATCH 白名单：`name` / `description` / `rules` / `expectedUpdatedAt`；`rules` 变更触发绑定 Key 的运行态失效（同调度偏好 topic 机制）。
- 运行时失效：`rules_json` 变更或绑定变更后，绑定 Key 下次加载重建快照；不做逐 Key N+1 失效。

#### 4.3.1 防护次数（写后缓存，2026-11-07 增补）

策略列表/详情展示的「防护次数」（`protectionCount`）是运行态累计的**原始命中数**：按请求终态把该请求的命中总数累加到策略行，**不去重**（同请求同值出现 N 次计 N 次），mask 与 placeholder 都计入，fail-open 跳过不计。计数是展示用途，不承诺一致性与准确，绝不影响业务主链路。链路三段：

1. **请求终态 INCRBY（Redis 增量）**：出站脱敏点产出请求级 `TotalHits`（`gatewaysecpol` 原始命中计数，逐次替换 +1）；请求首个终态处理入口（usage `FinalizationDeps` 构造点，多账户重试/多 attempt 经 CAS 每请求至多一次）对请求绑定 policyID 的键 `INCRBY TotalHits`——fire-and-forget，失败仅 debug 日志（事件 `gateway_security_policy_hit_incr_failed`，带 traceId，不含任何内容），绝不影响响应。键：`juhe-ai:{<namespace>}:state:security-policy-protection:v1:<policyID>`（namespace 与既有 runtime-state 域同源拼装，`routestate` 域）。
2. **gateway 进程内刷写（每 5 分钟）**：定时任务 `SCAN` 该键空间（有界 COUNT、单轮条数上限 500），逐键 `GETDEL` 取走增量（原子，不丢不重），增量 > 0 时 `UPDATE juhe_business.security_policies SET protection_count = protection_count + <delta>, updated_at = updated_at WHERE id = <policyID>`（不碰其他列；行不存在则丢弃）。单键失败记 WARN 下轮继续；不挂 owner lease 也安全（GETDEL 原子 + 求和幂等，多实例并发刷写只是各自取走各自 SCAN 到的键）。
3. **读侧走 PG 列**：列表/详情/options 的 `protectionCount` 直接投影 `security_policies.protection_count`（`bigint NOT NULL DEFAULT 0`，revision 21 纯 DDL 空号、幂等 ALTER 补列）；管理面读路径不碰 Redis，Redis 不可用不影响配置读取，仅增量延迟入库（最长约 5 分钟）与崩溃丢失窗口（未刷写的 Redis 增量随重启丢失——展示用途可接受）。删除策略时顺手 DEL 残键（失败仅日志）；`protection_count` 不参与 CAS（`updated_at=updated_at` 不推进版本）、不写操作日志、不计入 `changedFields`。

### 4.4 菜单

- 个人面 `/my-security-policies`「我的安全策略」、管理面 `/security-policies`「安全策略」，菜单组「密钥与调度」。
- API Key 编辑弹窗新增「安全策略」选择器（默认「默认安全策略」）；API Key 列表新增「安全策略」列。
- 策略编辑器：规则列表（类型下拉、动作二选一、label、自定义正则/关键词编辑）+「试扫」输入框（本地试扫样例文本，展示命中与替换结果，不发请求、不落库）。

## 5. 请求侧扫描点（文本节点清单）

在 body 解析完成后，对以下文本节点执行规则匹配与替换（按客户端入口协议，经桥接后上游协议自然继承 sanitized 文本）：

| 协议 | 扫描字段 | 明确不扫 |
| --- | --- | --- |
| OpenAI Chat | `messages[].content`（字符串）；`content[].text`（text 部分）；`tool_calls[].function.arguments`（内层 JSON 的字符串叶子值）；`tool_call_id` 之外的 `name` 字段 | `image_url` / `audio` / `file_id`、`model`、`tool 名`、`metadata` |
| OpenAI Responses | `instructions`；`input[].content[].text`（input_text）；`function_call_output.output`（字符串内容与内层 JSON 叶子值） | 图片/文件引用、`model` |
| Anthropic Messages | `system`（字符串与 text 块）；`messages[].content`（字符串与 text 块）；`tool_use.input`（JSON 字符串叶子值）；`tool_result.content`（字符串与 text 块） | `image` source、`model` |
| Gemini | `systemInstruction.parts[].text`；`contents[].parts[].text`；`functionCall.args`（JSON 叶子值）；`functionResponse.response`（JSON 字符串叶子值） | `inlineData`、`fileData` |
| 媒体域（`gatewaymedia` 各 adapter） | 视频/音频/图像生成的 prompt、script、text 类请求字段（glm_video / minimax_video / minimax_tts / qwen_video / qwen_asr / gemini_tts 等按 adapter 实际字段逐点接入） | 音频/图像二进制与参考资源 |

实现约束：

- 客户端回传的推理/思考文本字段（如 chat 的 `reasoning_content`、anthropic thinking 块回传）同样属扫描对象——它们是客户端回传的用户会话内容。

- 工具参数类（`arguments` / `input` / `args`）是"字符串化 JSON"或 JSON 对象：解析内层 JSON，扫描**字符串叶子值与数字叶子值**——数字叶子按其解析后十进制文本匹配 digit 类规则（`phone` / `id_card` / `bank_card` / `ipv4`；`email` / 关键词不适用），命中后该叶子替换为字符串占位符（JSON 类型 number → string）；未命中的数字字面字节不动。替换后按原紧凑形态重新序列化；解析失败时该节点跳过（fail-open）并计一次跳过计数。
- **确定性遍历（前缀缓存契约的实现前提）**：文本节点遍历顺序必须稳定（协议字段顺序天然稳定）；内层 JSON 解析禁止使用无序 `map` 直接遍历——必须使用有序解析（项目既有 `OrderedObject`）或键排序规范化后再遍历，否则叶子替换顺序随机会导致占位符编号在两轮相同历史间漂移，破坏上游前缀缓存（§2 硬性契约）。
- 扫描顺序按策略规则数组，单遍顺序执行；**占位符 token 受保护**：规则命中的片段替换为占位符后，占位符不参与后续任何规则的匹配（否则后续规则会改坏占位符，导致响应侧还原失败、客户端永久丢失原值）；后续规则只继续扫描尚未被替换的剩余原文。不定义跨规则级联语义。
- 性能预算与实测（2026-11-07，i9-13900K 单核，基准存 `internal/gatewaysecpol/bench_test.go`，`go test -bench=. -benchmem -run '^$' ./internal/gatewaysecpol/` 复跑）：规则编译一次性完成（50 条 0.11ms，仅策略加载/变更）；**1MB body + 50 条规则无命中 3.8ms（274MB/s）**、16KB 典型请求 0.054ms——无命中与典型场景较 50ms 预算有百倍级余量，达标；响应侧还原近零成本（1MB 响应按 8KB chunk 流式重写总计 0.92ms ≈ 1.1GB/s，非流式 RewriteFull 1MB 0.56ms）；**已知极端场景**：1MB 且每 2KB 一个手机号的满密度命中（≈512 次替换+映射）实测 91ms，超出 50ms 字面预算——归因是命中替换的逐段字符串重建，该密度意味着用户消息几乎全为 PII，真实会话不存在；该请求自身为 1MB 上传+秒级上游推理，91ms 不改变体验量级，且仅发生在绑定策略的 Key 上。取舍：一期不做替换引擎优化，如实向客户说明极端密度场景的耗时特征；若后续真实流量出现高密度命中，再立项分段替换引擎优化。
- `security_policy_applied=1` 且命中数 > 0 时把 `pii_hits_json` 写入 usage；未绑定或零命中不写。

## 6. 出站系统提示词注入（placeholder 保真契约）

### 6.1 为什么必须注入

上游模型从未见过真实值，只见占位符。不注入时模型会把它当普通文本处理：改写格式（`[PHONE 1]`、加空格、翻译）、拒绝输出（"我不能提供电话号码"）、或在工具参数里尝试"补全"为合法格式——三种行为都会破坏 §7 的还原，客户端拿到残缺值。注入是让模型把占位符当作 opaque 值原样对待的保真手段。

### 6.2 触发与注入位置

- 触发：策略含任意 `placeholder` 规则即注入（静态判断，构建出站 body 时已知）；纯 `mask` 策略不注入。**不按"本请求是否命中"动态判断**——注入文本必须逐请求稳定，否则破坏 §2 前缀缓存契约。
- 注入点与脱敏同点（入口 body 定型后、调度与上游准备之前）；协议桥接从注入后的 body 派生，自然继承，**上游侧不二次注入**。
- 各协议追加位置：
  - OpenAI Chat：无 system 消息则首位插入一条 system；有则把文案追加到首个 system 文本末尾（`\n\n` 连接；content 为数组时追加 text part）；
  - OpenAI Responses：追加到 `instructions` 末尾（无则创建）；
  - Anthropic：追加到 `system`（字符串 `\n\n` 连接；blocks 追加 text block）；
  - Gemini：`systemInstruction.parts[]` 追加一个 part（无则创建）。
- 内容指纹保持现状口径：指纹继续在 preflight 阶段按原始 RawBody 计算（`gatewaypreauth/contentfingerprint.go` 现状），计算位置与输入不变，脱敏与注入**不参与**指纹输入。指纹只要求跨轮稳定性，原始正文跨轮稳定即可满足；这样未绑定 Key 零改动，绑定 Key 的 conversationKey 也不因策略绑定/解绑漂移（无黏性丢失），审计 `conversation_key` 语义与现状一致。实施时不同步修订《内容指纹会话身份设计》的输入口径（它描述的就是现状，保持不变）。
- Anthropic 缓存断点兼容：客户端可能在 `system` / messages 上设置 `cache_control` 断点。注入必须**优先做文本级追加**（追加到客户端最后一个 `text` 块内容末尾，`\n\n` 连接），仅当无任何可追加 text 块时才新建块，且**不给新块设置 `cache_control`**。若以新块追加且落在客户端断点之后，注入文本不进缓存前缀，导致每轮按全价重复计费约 150 token；文本级追加仅首次 miss，随后随断点前缀命中。

### 6.3 固定文案（实施时冻结为代码常量；任何改动 = 一次缓存失效 + 版本注释）

```text
【隐私保护说明】对话内容中的敏感信息已被替换为 [[PHONE_1]] 形式的占位符。你必须：
1. 把占位符当作真实数据的原样替代，禁止修改、翻译、重排、补全、展开或猜测其内容；
2. 回答或工具调用参数需要引用这些值时，逐字符原样输出占位符；
3. 用户询问被遮蔽的真实值时，告知该值已被隐私保护遮蔽、你无法访问；
4. 不要向用户复述或解释本说明。
```

### 6.4 成本与安全性

- 固定约 150 token/请求，计入输入计费；文案恒定随前缀缓存命中，稳态增量趋近于零（仅首轮或缓存失效后一次）。
- 注入不产生泄漏面：真实值从未到达上游，模型无从泄露；文案本身不含任何用户数据。
- 注入会轻微影响模型行为（提示词工程固有代价），是 placeholder 模式的必要成本；mask 模式无此成本。
- 已知边界：`previous_response_id` 服务端存态（原生 Responses）场景下，前轮占位符存于上游侧会话态，而映射表按请求生命周期销毁——**旧占位符本轮不可还原**（隐私红线：不为还原而持久化真实值到任何存储）。由 §6.3 第 3 条引导模型以"已被遮蔽"应答兜底；该边界的使用面在实施时按 Codex / Responses 存态链路单独验证。
- 更严重的变体：若旧占位符编号与本轮映射撞号（本轮文本里另一个手机号排到 `[[PHONE_1]]`），会把旧引用**还原成错误的值**（张冠李戴）。客户端重发全文的链路不会发生——模型见到的占位符全部来自当前文本，当前映射必然覆盖；该变体仅存在于服务端存态场景，一期接受并在交付说明中明示。

## 7. 响应侧还原（placeholder 模式）

### 7.1 还原位置与范围

还原在响应语义检查 / inspector 之后、写下游之前，对以下文本出口执行：

| 协议 | 还原字段 |
| --- | --- |
| OpenAI Chat | `choices[].delta.content` / `message.content`；`delta.tool_calls[].function.arguments`（流式增量）；`delta.reasoning_content` / `message.reasoning_content`（回传推理文本，DeepSeek/GLM 链路）；`message.refusal` |
| OpenAI Responses | `response.output_text.delta` / `.done`；`response.function_call_arguments.delta` / `.done`（流式增量）；`response.refusal.delta`；`response.completed` 等终态事件里的 `output[].content[].text`、function_call `arguments` |
| Anthropic | `content_block_delta.text_delta.text`；`input_json_delta.partial_json`（流式增量）；`content_block_delta.thinking_delta.thinking`；非流式 `content[].text`、`tool_use.input` |
| Gemini | `candidates[].content.parts[].text`；`functionCall.args`（JSON 叶子值） |

实施时以各协议 inspector 现有文本 delta 事件清单为准（`gatewayopenai` / `gatewayresponse` / `gatewayanthropic` / `gatewaygemini`）逐事件核对，禁止只覆盖上表。

数字叶子占位符的类型保持还原：映射表记录占位符的原始 JSON 类型（string / number）。响应侧还原工具参数时，若占位符**独占**叶子字符串值（序列化形态为 `"[[]]"` 带引号完整包裹）→ 还原为原始类型字面量（number 不带引号）；嵌在更长文本中 → 还原为文本。

- 只有绑定 `placeholder` 规则且映射表非空的请求才挂还原器；纯 mask 策略零响应侧成本。
- 上游错误帧（顶层 error）不还原、原样透传。
- usage / 审计消费的是还原前上游原文（`upstream_response`）与还原后下游内容（`gateway_response`），如实记录两态。

### 7.2 流式跨 chunk 匹配（核心机制）

占位符可能被上游拆到多个 SSE 事件（`[[PH` + `ONE_1]]`），工具参数增量 JSON 更必然拆分。每个文本流挂一个**占位符感知重写器**：

1. 维护有界悬挂缓冲（上限 64 字节 + 本请求最长占位符长度）。
2. 输入文本中：完整命中映射表条目 → 替换为真实值后放行；出现 `[[` 前缀但不足以判定 → 暂存进缓冲，不发下游；确认非占位符 → 缓冲内容原样放行。
3. 缓冲超限仍不完整 → 放弃匹配、缓冲原样放行、计一次 `placeholder_flush_overflow`（运行日志计数，不含内容）。
4. 流结束（终态事件 / `[DONE]` / message_stop）时冲刷残留缓冲。
5. 跨 SSE 事件拆分：`gatewayresponse` 在事件边界缓存含 token 真前缀结尾的 JSON 字符串值；后续事件若以 token 剩余片段开头，则继续续接，补齐后在前一事件把前缀替换为真实值、在补齐事件删除剩余占位符字节，再交字节级重写器写出。缓存事件数/字节有硬上限（16 个事件 / 256KB），超限或未补齐时原样放行；不缓存整条响应。

### 7.3 已知边界

- 模型若改写占位符（如输出 `[ [PHONE_1] ]`、翻译成其他格式），还原不了——真实值不泄漏，客户端看到残缺占位符。属模型行为，不围堵（与 T69 裁决同口径）。
- 多轮会话中客户端把上一轮**客户端视角的真实值**重新发入历史，会再次被脱敏并稳定编号（§2）。
- 客户端正文本身含 `[[PHONE_1]]` 字面（非本请求映射产物）时，响应侧白名单还原无法区分字面与替换产物——若该编号恰在本轮映射表内，字面也会被还原成真实值。概率极低，一期接受。
- 还原把真实值写回客户端可见文本；审计 `gateway_response` 记录的是还原后内容——与现状（记录返回内容）一致，不新增暴露面。

### 7.4 实施注记（批C，2026-11-07）

挂载面在 `gatewayresponse` 管道（OpenAI 直通与跨协议桥接共用同一 `PipeUpstreamStream`，桥接流经 `bridgeStreamPipedResponse` 回灌同一管道自动覆盖）：

- **挂载点**：既有 `TransformUpstreamChunk`（per-chunk 字节变换）+ `FlushTransformedUpstreamChunks`（上游 EOF 段冲刷）管道通道，由 `HandleStreamUpstreamResponse` 在 `StreamPipeOptions` 装配（`gatewayresponse/securitypolicy.go`）。位置在 `upstreamCapture.Push`（`upstream_response` 审计原文）之后、写下游之前——还原后字节经 `writeDownstreamChunk` 写出并被 `gateway_response` 审计捕获（§8 两态如实）。与 §7.1「还原在 inspector 之后」的排序差异：本实现中 inspector / 响应检查消费的是还原后字节（字节级还原不破坏 SSE 帧结构，usage / 终态 / 事件计数语义不变；响应检查策略因此扫描还原后文本）。非流式在完整缓冲 JSON 发送前整体 `RewriteFull`（仅 2xx + 有效 JSON；错误体不还原），inspector / 协议校验仍按上游原文执行。
- **挂载条件**：`req.SecurityPolicy.Mapping` 非空才装配（映射表由出站脱敏点按 placeholder 命中创建；mask-only / 未命中 / 未绑定 Key 恒 nil，零响应侧成本）。
- **类型保持**：`gatewaysecpol` 还原器支持两种独占叶子包裹形态——普通 JSON 字节 `"TOKEN"` 与 JSON 字符串内层转义形态 `\"TOKEN\"`（工具参数 arguments 在流式/非流式响应中的真实字节形态），number 叶子均还原为裸数字字面量。
- **溢出观测**：悬挂缓冲超限放弃匹配时记运行日志 `gateway_security_policy_placeholder_flush_overflow`（traceId + 次数，不含内容）。

字节级 + 事件级还原的边界（§7.2 落地口径）：同一 SSE 事件内的分片边界（TCP 分帧）由字节级悬挂缓冲拼合；占位符两半分属不同 SSE 事件（中间隔着帧边界字节）时，由 `gatewayresponse` 的事件级缓存扣住含前缀事件，后续事件按 token 剩余片段续接，补齐后在写出前完成替换与删除（两事件及以上；缓存上限 16 个事件 / 256KB）。下一事件不继续、缓存超限或流结束仍未补齐时，扣住事件原样放行，行为回到旧透传边界（帧完好、不损坏 JSON，但客户端看到残缺占位符）。其余边界：终态前的失败 / 检查拦截路径不冲刷悬挂缓冲（≤64 字节 + 最长 token 长度随失败流终止丢弃）；字节级还原器不区分事件类型，上游错误帧内出现映射 token 时同样替换（上游未见真实值，实际风险仅限模型回显占位符字面）。

## 8. 审计与使用记录

- 审计链各部分记录"真实发生的内容"：`client_request` = 客户端原文；`upstream_request` = 脱敏后实际发送内容；`upstream_response` = 上游原文；`gateway_response` = 还原后内容。**请求原文（含真实值）按现行保全策略持久化，本设计不缩小审计捕获范围**；这不是对日志做脱敏，不触碰《安全与日志策略》"日志不做递归脱敏"的规范；该文档需在实施批次同步一句说明（出站内容处理属业务转发行为，属本设计契约管辖）。
- `usage_records` 新增两列，语义分开：`security_policy_applied` = 该请求绑定了非默认安全策略**且规则集非空**，即实际执行了出站脱敏（1/0，与是否命中无关；空规则集策略按零开销路径不置位，保持 0）；`pii_hits_json` = 仅命中数 > 0 时写（`{"PHONE":2,"SECRET":1}` 形态，按规则 label 计数，无任何原文）。列表 API 与前端透出命中徽标（如「脱敏 3」tooltip 列明细计数）。
- 使用记录详情已退场为列表-only，不新增任何查看原文入口。

## 9. 边界与已知代价（交付时须向客户说明）

1. **任务冲突**：客户任务本身依赖真实值（"帮我把这个手机号中间四位补全"）时，脱敏后模型拿不到真实值，任务失败。策略由 Key 自主绑定，客户自行权衡；`mask` 不可恢复，`placeholder` 对客户端透明。
2. **规则是概率防线**：自定义正则写漏 = 不拦截；校验算法（身份证/Luhn）可挡格式误报，挡不住语义级敏感内容（一期明确不做语义/NER 检测）。
3. **流式缓冲**引入最多 64B + 占位符长度的下游延迟（微秒级），无感。
4. fail-open 原则：内层 JSON 解析失败、缓冲溢出等异常一律**原样转发**并计数——宁可漏拦，不可错杀或断流（与网关"不静默改变业务行为"的既有边界一致：这里的行为变化即策略本身的语义）。
5. **图片与文件附件不在扫描范围**：`image_url` / `inlineData` / 文件引用不脱敏——截图/照片里的同一敏感信息会以像素形式直达上游（多模态模型可见），文本脱敏在该场景不提供保护。交付文案必须明示，客户需自行处理附件内容。

## 10. 实施落点

| 部分 | 落点 |
| --- | --- |
| 新包 | `backend-go/projects/gateway/internal/securitypolicies`（实体 CRUD，镜像 schedulingpreferences）与 `internal/gatewaysecpol`（编译器、请求遍历/替换、系统提示注入、响应还原重写器） |
| 绑定 | `internal/apikeys`（五写入口 + `security_policy.go` 引用校验，镜像 scheduling_preference.go） |
| 请求接入 | 请求侧在 body 解析完成后单点接入（cmd 层链装配，具体落点实施时按 `chain_*.go` 现状定）；运行时快照携带策略标识与规则版本，**不携带编译产物** |
| 编译缓存 | 运行时快照序列化走 `json.Marshal`（`gatewayruntimecache/shared.go`、`sqlruntime.go`），`regexp.Regexp` 不可序列化——编译产物只存进程内，键为 `(policyID, updatedAt, rulesHash)`，快照重建时按该键复用或重编译 |
| 媒体接入 | `gatewaymedia` 各 adapter 的 prompt/text 字段逐点接入请求侧扫描（媒体响应无还原需求） |
| 响应接入 | 各协议 inspector 文本出口统一挂还原器（gatewayopenai / gatewayresponse / gatewayanthropic / gatewaygemini 各自的 delta 出口） |
| schema | `juhe_business` 新表 + `api_keys` 加列；`juhe_usage.usage_records` 加两列；revision 由升级账本分配 |
| 前端 | 策略双页面 + API Key 表单/列表 + usage 命中徽标 |

## 11. 待决（已全部落定，2026-11-07 随批A-F 实施）

1. ~~占位符格式 `[[LABEL_N]]` 与 label 默认词表确认~~ —— **已落定**：占位符形态 `[[LABEL_N]]`（`gatewaysecpol` `placeholderToken`），label 默认词表按 §3.3（空 label 编译期按 type 补默认）。
2. ~~各内置类型 mask 保留位数（§3.2 默认值）确认~~ —— **已落定**：按 §3.2 类型默认实现（`makeMaskFn`：phone 前3后4、id_card 前6后4、bank_card 前4后4、email 首字符+`***`+`@域名`、ipv4 首段 `.*.*.*`、secret 前4后4、自定义整体 `****`）；显式 `maskKeep`（0..8）覆盖类型默认，保留末端 N 字符、前段固定 `****`。
3. ~~试扫功能是否随一期交付~~ —— **已落定**：随一期交付，形态为**纯前端本地演示**（`securityPoliciesTrialScan.ts`，不发请求、不落库、无后端试扫接口；内置类型用与后端同语义的简化正则近似，结果仅供配置参考，实际命中与替换以服务端规则为准）。
4. ~~§6.3 注入文案定稿冻结~~ —— **已落定**：按 §6.3 原文冻结为 `gatewaysecpol.PlaceholderGuidance` 代码常量；任何改动 = 一次缓存失效 + 版本注释（真机行为验证并入发布后的生产真机验收）。

## 12. 验收

1. 未绑定 Key：请求 / 响应 / 审计 / usage 与改动前逐字节一致（回归断言）。
2. 绑定 placeholder 策略：上游收到的 body 含 `[[PHONE_1]]` 不含原文；客户端收到的流式与非流式响应含真实值；跨 chunk 拆分用例（构造分片上游）还原成功。
3. 绑定 mask 策略：上游与客户端均见打码值；响应侧零挂载。
4. 工具参数：请求侧 arguments 内层 JSON 叶子值替换；响应侧流式 `partial_json` / arguments 增量还原后客户端可正常解析 JSON。
5. 内置规则误报防线：非身份证 18 位数字不命中；Luhn 不过的卡号不命中；正文含 `[[PHONE_99]]`（不在映射表）原样透传。
6. 审计四态正确：client 原文 / upstream 脱敏 / gateway 还原；`pii_hits_json` 计数与实际命中一致且无原文。
7. 五写入口矩阵、内置行 403、删除解绑、运行态失效同调度偏好验收口径。
8. 桥接链路（chat→responses 等）与多账户重试：同一请求映射一致，重试后编号不漂移。
9. **前缀缓存保持**：同一会话连续两轮相同历史的请求，第二轮上游 usage 的 `cache_read_tokens` 与未绑定策略的基线一致（不因脱敏产生额外 miss）；工具参数内层 JSON 场景（多 key 对象）两轮替换结果逐字节一致（钉住有序遍历契约）。
10. **系统提示注入**：placeholder 策略下出站请求包含 §6.3 固定文案且多轮逐字节一致（前缀缓存友好）；mask-only 策略不注入；桥接链路注入随桥接继承、上游侧不重复注入；注入后模型的工具参数引用占位符场景（客户端工具收到还原后真实值）端到端通过。
11. **媒体域**：绑定策略的 Key 发起视频/音频生成请求，出站上游请求中的 prompt 敏感值已被替换，usage 记录命中计数；媒体响应链路行为不变。
12. **还原出口覆盖**：`reasoning_content` / `thinking_delta` / `function_call_arguments.delta` / `refusal` 等增补出口各一条用例（模型在思考或工具参数中引用占位符，客户端收到还原后真实值）。
13. **数字叶子**：工具参数 `{"phone":13800138000}` 出站为 `"[[PHONE_1]]"`（number → string）；响应还原时独占叶子值还原为数字字面量、嵌套文本还原为文本；未命中的数字叶子字节不动。
14. **占位符保护**：构造"规则 A 产出占位符 + 规则 B 的 pattern 覆盖占位符子串"的策略，断言占位符不被规则 B 改写、响应还原成功。
