import type { AccountCapabilityDomain } from './accountCapabilityDomains'
import { capabilityDomainIsMedia } from './accountCapabilityDomains'

/**
 * 媒体域（视频/音频/图像）表单的域内空模型提示。
 *
 * 媒体账户与对话账户共用同一套向导/工作台表单壳（2026-11-11 统一），
 * 本文件只保留域内模型候选的展示逻辑；上游接口能力不再由用户勾选，
 * 媒体账户恒等于该供应商该域允许集。
 */

/**
 * v8-D 域内空模型列表提示：选中供应商在当前能力域的目录候选「加载后确认为
 * 空」（settled 且非加载中）时给出引导文案。候选是懒加载，弹窗打开加载一次、
 * 切供应商即清空、下拉展开/搜索再加载——未加载的空不是真没有货，settled 前
 * 返回 undefined。对话域不适用本提示。
 */
export function mediaDomainModelAvailabilityHint(input: {
  capabilityDomain: AccountCapabilityDomain
  optionCount: number
  loading: boolean
  settled: boolean
}): string | undefined {
  if (!capabilityDomainIsMedia(input.capabilityDomain)) return undefined
  if (input.loading || !input.settled || input.optionCount > 0) return undefined
  return '该供应商在此域暂无可用目录模型，可切换供应商，或先在模型目录中为该供应商添加此域模型'
}
