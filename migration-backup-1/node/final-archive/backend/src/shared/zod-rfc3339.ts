import { z } from 'zod'

import { canonicalizeRfc3339Instant } from './rfc3339.js'

/**
 * HTTP schema for absolute timestamps. It accepts RFC3339 `Z` or numeric
 * offsets, rejects timezone-less values, and exposes only canonical UTC.
 */
export function rfc3339InstantSchema(message = '时间必须是带 Z 或数值 offset 的 RFC3339 时间') {
  return z.string()
    .trim()
    .min(1, message)
    .refine((value) => canonicalizeRfc3339Instant(value) !== undefined, message)
    .transform((value) => canonicalizeRfc3339Instant(value)!)
}
