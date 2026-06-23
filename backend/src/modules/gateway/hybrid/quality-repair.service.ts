import type { Request } from 'express'

import { replaceGatewayJsonBody, type GatewayRawBodyRequest } from '../request/body.js'
import { parseGatewayJsonBodyInWorker } from '../request/json-parser.js'
import type { HybridQualityInspectionOutcome } from './quality-inspection.service.js'

const hybridQualityRepairInstructionMaxChars = 2000

export async function appendHybridQualityRepairInstruction(
  req: Request,
  quality: HybridQualityInspectionOutcome | undefined,
  signal?: AbortSignal
): Promise<boolean> {
  if (!quality?.result) return false
  const body = await mutableGatewayJsonBody(req, signal)
  if (!body) return false
  const instruction = buildHybridQualityRepairInstruction(quality)
  if (Array.isArray(body.messages)) {
    replaceGatewayJsonBody(req, {
      ...body,
      messages: [
        ...body.messages,
        {
          role: 'user',
          content: instruction
        }
      ]
    })
    return true
  }
  if (typeof body.input === 'string') {
    replaceGatewayJsonBody(req, {
      ...body,
      input: `${body.input}\n\n${instruction}`
    })
    return true
  }
  if (Array.isArray(body.input)) {
    replaceGatewayJsonBody(req, {
      ...body,
      input: [
        ...body.input,
        {
          type: 'message',
          role: 'user',
          content: [
            {
              type: 'input_text',
              text: instruction
            }
          ]
        }
      ]
    })
    return true
  }
  return false
}

async function mutableGatewayJsonBody(req: Request, signal?: AbortSignal): Promise<Record<string, unknown> | undefined> {
  const request = req as GatewayRawBodyRequest
  const body = request.body !== undefined
    ? request.body
    : request.gatewayParsedJsonBodyAvailable
      ? request.gatewayParsedJsonBody
      : undefined
  if (isJsonRecord(body)) {
    return { ...body }
  }
  if (!request.rawBody?.length) return undefined
  const parsed = await parseGatewayJsonBodyInWorker(request.rawBody, 30000, signal)
  return isJsonRecord(parsed) ? { ...parsed } : undefined
}

function buildHybridQualityRepairInstruction(quality: HybridQualityInspectionOutcome): string {
  const result = quality.result
  const lines = [
    '上一次回答没有通过混合路由质量评分。请基于原始用户需求重新给出最终答案，不要解释评分过程。',
    '质量评分反馈：',
    result?.failureType ? `- 问题类型：${result.failureType}` : undefined,
    typeof result?.score === 'number' ? `- 质量分：${result.score}` : undefined,
    result?.reason ? `- 失败原因：${result.reason}` : undefined,
    result?.retryRecommendation ? `- 评分建议：${result.retryRecommendation}` : undefined,
    '修复要求：补齐遗漏内容，修正不符合要求的格式、字段、工具参数或文件内容；如果原请求要求严格 JSON、代码、补丁或结构化输出，本次只输出符合要求的最终结果。'
  ].filter((line): line is string => Boolean(line))
  const text = lines.join('\n')
  return text.length <= hybridQualityRepairInstructionMaxChars
    ? text
    : `${text.slice(0, hybridQualityRepairInstructionMaxChars)}...`
}

function isJsonRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
