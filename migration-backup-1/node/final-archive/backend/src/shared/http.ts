import type { Response } from 'express'

export interface ApiResponse<T> {
  data: T
  message?: string
}

export function ok<T>(data: T, message?: string): ApiResponse<T> {
  return { data, message }
}

export function badRequest(message: string): { message: string } {
  return { message }
}

export function firstIssueMessage(error: { issues: Array<{ message?: string }> }, fallback: string): string {
  return error.issues[0]?.message ?? fallback
}

export type ParseResult<T> =
  | { success: true; data: T }
  | { success: false; message: string }

interface SafeParser<T> {
  safeParse(value: unknown): { success: true; data: T } | { success: false; error: { issues: Array<{ message?: string }> } }
}

export function parseOrBadRequest<T>(schema: SafeParser<T>, value: unknown, fallback: string): ParseResult<T> {
  const parsed = schema.safeParse(value)
  if (!parsed.success) {
    return { success: false, message: firstIssueMessage(parsed.error, fallback) }
  }
  return { success: true, data: parsed.data }
}

export function sendBadRequest(res: Response, message: string): void {
  res.status(400).json(badRequest(message))
}

export function sendNotFound(res: Response, message: string): void {
  res.status(404).json({ message })
}
