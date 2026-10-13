/**
 * 能力载荷安全读取（媒体工作室设计 §8 强制点 1）：capabilities 是表单控件的
 * 唯一声明源——未声明/类型不符的键一律视为缺项，调用方按"缺项不渲染"处理。
 */

export function studioCapabilityList(caps: Record<string, unknown> | undefined | null, key: string): string[] {
  const raw = caps?.[key]
  if (!Array.isArray(raw)) return []
  return raw.filter((item): item is string => typeof item === 'string' && item.trim().length > 0)
}

/** 标量档位（string | number，如视频 secondsOptions 的 4 / "4s"）。 */
export function studioCapabilityScalarList(caps: Record<string, unknown> | undefined | null, key: string): Array<string | number> {
  const raw = caps?.[key]
  if (!Array.isArray(raw)) return []
  return raw.filter((item): item is string | number => typeof item === 'string' || typeof item === 'number')
}

export function studioCapabilityFlag(caps: Record<string, unknown> | undefined | null, key: string): boolean {
  return caps?.[key] === true
}

/** [min, max] 数值区间（如 audio speedRange）；结构或数值非法视为缺项。 */
export function studioCapabilityRange(caps: Record<string, unknown> | undefined | null, key: string): [number, number] | undefined {
  const raw = caps?.[key]
  if (!Array.isArray(raw) || raw.length !== 2) return undefined
  const min = Number(raw[0])
  const max = Number(raw[1])
  if (!Number.isFinite(min) || !Number.isFinite(max) || min > max) return undefined
  return [min, max]
}

/** 目录声明的单次单价（成本预估输入）；未声明返回 undefined → 前端显式"无法估算"。 */
export function studioCapabilityUnitPrice(caps: Record<string, unknown> | undefined | null): number | undefined {
  const raw = caps?.unitPrice
  if (typeof raw !== 'number' || !Number.isFinite(raw) || raw < 0) return undefined
  return raw
}
