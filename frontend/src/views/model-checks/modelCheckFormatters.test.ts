import { describe, expect, it } from 'vitest'
import { modelCheckQuizSummary } from './modelCheckFormatters'

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
