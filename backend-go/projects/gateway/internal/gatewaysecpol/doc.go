// Package gatewaysecpol 是出站内容脱敏的纯函数核心库。权威契约：
// docs/functions/APIKey安全策略与出站内容脱敏设计.md，本包实现其 §3 规则编译、
// §5 请求侧扫描遍历与替换、§6 出站系统提示注入、§7 响应侧占位符还原。
//
// 职责边界（批次拆分）：
//   - Compile：把 securitypolicies 存储的 rules_json 编译为可并发复用的
//     CompiledPolicy；内置规则的 pattern 与校验算法只在本层定义（设计 §3.2）。
//   - SanitizeBody：按协议族字段清单遍历文本/数字叶子并替换（§5）；含
//     placeholder 规则的策略在替换完成后注入 §6.3 固定文案（注入文本自身
//     不被扫描）；解析失败 / 未知协议族 / body 非 JSON 对象一律 fail-open
//     原样返回（§9 第 4 条：宁可漏拦，不可错杀或断流）。
//   - Mapping：请求级占位符映射表（§2），随请求上下文传播、终态销毁；非
//     并发安全，只在单 goroutine 请求链内使用。
//   - StreamRewriter / RewriteFull：响应侧字节级还原（§7.1 白名单匹配 +
//     类型保持；§7.2 有界悬挂缓冲跨 chunk 匹配）。
//
// 不在本包：实体 CRUD 与 rules_json 提交校验（internal/securitypolicies）、
// 链路装配接入（cmd 层批次）、媒体 adapter 逐点接入、各协议 inspector
// 还原出口挂载。
//
// 硬性契约（§2 前缀缓存保持）：替换是确定性纯函数——同一原文在同一策略版本
// 下永远得到同一替换结果，遍历顺序确定。内层 JSON 一律有序解析
// （orderedjson.go 的 orderedObject，参照 gatewayusage 的 OrderedObject 先例），
// 禁止 map 随机序；占位符编号按同标签首次出现顺序从 1 递增，同一原文片段
// 复用同一编号；序列化数字叶子保留字面文本（未命中数字字节不动）。
//
// 已知边界（§7.3 / §9）：还原是字节级白名单替换，模型改写占位符、字面
// [[LABEL_N]] 恰在映射表内、previous_response_id 存态旧占位符等场景按设计
// 接受，不围堵；规则是概率防线。
package gatewaysecpol
