import { describe, expect, it } from 'vitest'
import type { ModelCheckCheckResult } from '../../types/domain'
import {
  checkMessage,
  modelCheckQuizSummary,
  questionBankDateTimeText,
  questionCreatorText,
  questionDraftKey,
  questionTestVerdictColor,
  questionTestVerdictText,
  splitTrustReasonCodes,
  translateProbeMessage,
  trustCoverageCodeText,
  trustReasonCodeText
} from './modelCheckFormatters'

describe('modelCheckQuizSummary 未执行判定', () => {
  it('以后端 executed=false 为准判定未执行（新后端契约：未执行时 score=0）', () => {
    const summary = modelCheckQuizSummary({
      customQuiz: { enabled: true, executed: false, score: 0, maxScore: 31, deduction: 0, items: [] }
    })
    expect(summary?.executed).toBe(false)
    expect(summary?.score).toBe(0)
  })

  it('executed=true 时按后端事实判定已执行', () => {
    const summary = modelCheckQuizSummary({
      customQuiz: {
        enabled: true,
        executed: true,
        score: 25,
        maxScore: 31,
        deduction: 6,
        items: [{ questionId: 'q1', title: '题目一', verdict: 'passed', reason: '回答正确' }]
      }
    })
    expect(summary?.executed).toBe(true)
    expect(summary?.score).toBe(25)
    expect(summary?.deduction).toBe(6)
  })

  it('历史缺陷数据（无 executed 字段、执行项空但 score=31）兜底判定为未执行', () => {
    const summary = modelCheckQuizSummary({
      customQuiz: { enabled: true, score: 31, maxScore: 31, deduction: 0, items: [] }
    })
    expect(summary?.executed).toBe(false)
    expect(summary?.score).toBe(31)
    expect(summary?.items).toEqual([])
  })

  it('历史数据无 executed 字段且执行项全部为 unavailable/无 verdict（含全请求失败形态）时兜底判定为未执行', () => {
    const summary = modelCheckQuizSummary({
      customQuiz: {
        enabled: true,
        score: 31,
        maxScore: 31,
        deduction: 0,
        items: [
          // 后端全请求失败转 skipped 的真实形态：questionId 非空、verdict=unavailable
          { questionId: 'q1', title: 'Q1', verdict: 'unavailable' },
          { questionId: 'q2', title: 'Q2', verdict: 'unavailable' }
        ]
      }
    })
    expect(summary?.executed).toBe(false)
    expect(summary?.score).toBe(31)
  })

  it('历史数据无 executed 字段且存在 passed/failed 判定项时判定已执行', () => {
    const summary = modelCheckQuizSummary({
      customQuiz: {
        enabled: true,
        score: 31,
        maxScore: 31,
        deduction: 0,
        items: [
          { questionId: 'q1', title: 'Q1', verdict: 'unavailable' },
          { questionId: 'q2', title: 'Q2', verdict: 'failed' }
        ]
      }
    })
    expect(summary?.executed).toBe(true)
  })

  it('历史数据无 executed 字段但保留有效执行项时判定已执行并解析执行项', () => {
    const summary = modelCheckQuizSummary({
      customQuiz: {
        enabled: true,
        score: 25,
        maxScore: 31,
        deduction: 6,
        items: [
          { questionId: 'q1', title: '题目一', verdict: 'passed', reason: '回答正确' },
          { questionId: 'q2', title: '题目二', verdict: 'unavailable', reason: '无法判定' }
        ]
      }
    })
    expect(summary?.executed).toBe(true)
    expect(summary?.items).toEqual([
      { questionId: 'q1', title: '题目一', verdict: 'passed', reason: '回答正确' },
      { questionId: 'q2', title: '题目二', verdict: 'unavailable', reason: '无法判定' }
    ])
  })

  it('customQuiz 缺失时返回 undefined', () => {
    expect(modelCheckQuizSummary(undefined)).toBeUndefined()
    expect(modelCheckQuizSummary({})).toBeUndefined()
  })
})

describe('questionBankDateTimeText 题库时间去毫秒', () => {
  it('去除秒后的毫秒段', () => {
    expect(questionBankDateTimeText('2026-10-08T09:16:02.389Z').endsWith('.389')).toBe(false)
    expect(questionBankDateTimeText('2026-10-08T09:16:02.389Z')).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/)
  })

  it('无毫秒的时间保持不变', () => {
    const value = questionBankDateTimeText('2026-10-08T09:16:02Z')
    expect(value).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/)
  })

  it('空值显示占位符', () => {
    expect(questionBankDateTimeText(undefined)).toBe('-')
  })
})

describe('questionCreatorText 提交人展示', () => {
  it('优先显示管理面透出的显示名', () => {
    expect(questionCreatorText({ createdByName: '造数-管理员用户', createdBy: 'mockdata_user_admin' })).toBe('造数-管理员用户')
  })

  it('显示名缺失时回退创建者 id，不静默消失', () => {
    expect(questionCreatorText({ createdBy: 'mockdata_user_admin' })).toBe('mockdata_user_admin')
    expect(questionCreatorText({ createdByName: '   ', createdBy: 'mockdata_user_admin' })).toBe('mockdata_user_admin')
  })
})

describe('questionTestVerdict 展示映射', () => {
  it('四种 verdict 各有中文文案', () => {
    expect(questionTestVerdictText('pass')).toBe('通过')
    expect(questionTestVerdictText('fail')).toBe('未通过')
    expect(questionTestVerdictText('invalid')).toBe('无法判定')
    expect(questionTestVerdictText('request_failed')).toBe('请求失败')
  })

  it('颜色只作辅助语义：四态各有 tag 色，文案始终保留', () => {
    expect(questionTestVerdictColor('pass')).toBe('green')
    expect(questionTestVerdictColor('fail')).toBe('red')
    expect(questionTestVerdictColor('invalid')).toBe('default')
    expect(questionTestVerdictColor('request_failed')).toBe('warning')
  })
})

describe('questionDraftKey 提交门禁内容指纹', () => {
  it('题面、参考答案或要点变化都导致键变化', () => {
    expect(questionDraftKey('q1', 'a', [])).not.toBe(questionDraftKey('q2', 'a', []))
    expect(questionDraftKey('q', 'a1', [])).not.toBe(questionDraftKey('q', 'a2', []))
    expect(questionDraftKey('q', 'a', ['k1'])).not.toBe(questionDraftKey('q', 'a', ['k2']))
    expect(questionDraftKey('q', 'a', ['k1', 'k2'])).not.toBe(questionDraftKey('q', 'a', ['k2', 'k1']))
  })

  it('题面与参考答案的首尾空白差异归一，不触发重测', () => {
    expect(questionDraftKey('  q  ', 'a', [])).toBe(questionDraftKey('q', 'a', []))
    expect(questionDraftKey('q', '\n a \t', [])).toBe(questionDraftKey('q', 'a', []))
  })
})

describe('splitTrustReasonCodes 覆盖缺口与异常码分组', () => {
  it('生产真实覆盖缺口数组全部归入 coverage，anomalies 为空', () => {
    const codes = ['stability', 'distribution', 'behavior_probe', 'long_context', 'sampling_statistics', 'token_integrity_partial', 'token_integrity_evidence_insufficient']
    const split = splitTrustReasonCodes(codes)
    expect(split.anomalies).toEqual([])
    expect(split.coverage).toEqual(codes)
  })

  it('异常码与未知码归入 anomalies，缺口码归入 coverage，保持输入顺序且不去重', () => {
    const split = splitTrustReasonCodes([
      'proportional_padding',
      'token_integrity_partial',
      'totally_unknown',
      'token_integrity_not_applicable',
      'model_response_evidence_unavailable',
      'cross_model',
      'proportional_padding'
    ])
    expect(split.anomalies).toEqual(['proportional_padding', 'totally_unknown', 'proportional_padding'])
    expect(split.coverage).toEqual(['token_integrity_partial', 'token_integrity_not_applicable', 'model_response_evidence_unavailable', 'cross_model'])
  })
})

describe('trustCoverageCodeText 覆盖缺口文案', () => {
  it('各后缀与裸族名按族名标签生成中文文案', () => {
    expect(trustCoverageCodeText('stability')).toBe('稳定性探针未覆盖')
    expect(trustCoverageCodeText('token_integrity_partial')).toBe('Token 用量诊断未形成完整证据')
    expect(trustCoverageCodeText('token_integrity_evidence_insufficient')).toBe('Token 用量诊断证据不足')
    expect(trustCoverageCodeText('sampling_statistics_not_applicable')).toBe('采样统计分析不适用')
    expect(trustCoverageCodeText('cross_model')).toBe('辅助模型对照未覆盖')
  })

  it('模型响应证据不可用与未知码兜底', () => {
    expect(trustCoverageCodeText('model_response_evidence_unavailable')).toBe('模型响应证据不可用')
    expect(trustCoverageCodeText('totally_unknown')).toBe('未知原因码：totally_unknown')
  })
})

describe('trustReasonCodeText 异常原因码文案', () => {
  it('既有映射保持不变', () => {
    expect(trustReasonCodeText('proportional_padding')).toBe('差分斜率疑似比例灌水')
    expect(trustReasonCodeText('undeclared_response_model_mismatch')).toBe('响应模型与请求不一致')
    expect(trustReasonCodeText('protocol_check_failed')).toBe('协议探针未通过')
  })

  it('生成侧真实异常码全量映射（trust.go cross_model_mismatch / token_integrity_anomaly）', () => {
    expect(trustReasonCodeText('cross_model_mismatch')).toBe('辅助模型对照不一致')
    expect(trustReasonCodeText('token_integrity_anomaly')).toBe('Token 用量诊断异常')
  })

  it('未知码回退未知原因码', () => {
    expect(trustReasonCodeText('totally_unknown')).toBe('未知原因码：totally_unknown')
  })
})

describe('translateProbeMessage 探针消息翻译', () => {
  it('七类后端英文消息映射为中文', () => {
    expect(translateProbeMessage('J3b probe timed out')).toBe('探针请求超时（上游未在预算内完成响应）')
    expect(translateProbeMessage('J3b probe canceled')).toBe('探针已取消')
    expect(translateProbeMessage('J3b upstream request failed: dial tcp 10.0.0.1:443: connect refused')).toBe('上游请求失败：dial tcp 10.0.0.1:443: connect refused')
    expect(translateProbeMessage('J3b upstream response read failed')).toBe('上游响应读取失败')
    expect(translateProbeMessage('J3b upstream returned HTTP 429')).toBe('上游返回 HTTP 429')
    expect(translateProbeMessage('J3b upstream response exceeded limit')).toBe('上游响应超过大小上限')
    expect(translateProbeMessage('J3b OAuth Codex response stream incomplete')).toBe('OAuth Codex 响应流未完成')
  })

  it('不匹配的消息原样返回', () => {
    expect(translateProbeMessage('some future english message')).toBe('some future english message')
    expect(translateProbeMessage('J3b upstream returned HTTP')).toBe('J3b upstream returned HTTP')
  })
})

describe('checkMessage 检测项消息三级回退', () => {
  const baseCheck: ModelCheckCheckResult = {
    id: 'c1',
    runId: 'r1',
    itemKey: 'custom_quiz.q1',
    itemType: 'custom_quiz',
    status: 'failed',
    score: 0,
    maxScore: 1,
    evidenceSummary: {},
    createdAt: '2026-10-12T00:00:00Z',
    updatedAt: '2026-10-12T00:00:00Z'
  }
  const makeCheck = (overrides: Partial<ModelCheckCheckResult>): ModelCheckCheckResult => ({ ...baseCheck, ...overrides })

  it('message 非空时优先消费并翻译探针英文消息', () => {
    expect(checkMessage(makeCheck({ evidenceSummary: { message: 'J3b probe timed out' } }))).toBe('探针请求超时（上游未在预算内完成响应）')
    expect(checkMessage(makeCheck({ evidenceSummary: { message: 'J3b probe timed out', error: 'J3b probe canceled' } }))).toBe('探针请求超时（上游未在预算内完成响应）')
  })

  it('message 空白时回退 error 键（custom_quiz 失败证据）且英文 error 被翻译', () => {
    expect(checkMessage(makeCheck({ evidenceSummary: { error: 'J3b upstream returned HTTP 401' } }))).toBe('上游返回 HTTP 401')
    expect(checkMessage(makeCheck({ evidenceSummary: { message: '   ', error: 'J3b upstream request failed: connect refused' } }))).toBe('上游请求失败：connect refused')
  })

  it('evidence 两级皆空时回退 check.errorMessage，全部为空返回 undefined', () => {
    expect(checkMessage(makeCheck({ evidenceSummary: {}, errorMessage: 'J3b probe canceled' }))).toBe('探针已取消')
    expect(checkMessage(makeCheck({ evidenceSummary: { message: '   ', error: '  ' }, errorMessage: '   ' }))).toBeUndefined()
    expect(checkMessage(makeCheck({ evidenceSummary: {} }))).toBeUndefined()
  })
})
