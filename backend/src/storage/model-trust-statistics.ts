export interface RobustVectorSummary {
  median: number[]
  mad: number[]
  q10: number[]
  q90: number[]
  retainedCount: number
  excludedCount: number
}

export function robustVectorSummary(vectors: number[][]): RobustVectorSummary {
  const width = vectors[0]?.length ?? 0
  if (!width || !vectors.length) return { median: [], mad: [], q10: [], q90: [], retainedCount: 0, excludedCount: 0 }
  const initial = vectorSummary(vectors, width)
  const retained = vectors.filter((vector) => robustVectorDistance(vector, initial.median, initial.mad) <= 6)
  const stable = retained.length >= Math.max(3, Math.ceil(vectors.length * 0.5)) ? retained : vectors
  const summary = vectorSummary(stable, width)
  return { ...summary, retainedCount: stable.length, excludedCount: vectors.length - stable.length }
}

export function robustVectorDistance(vector: number[], medianVector: number[], madVector: number[]): number {
  if (!vector.length || vector.length !== medianVector.length) return Number.POSITIVE_INFINITY
  const scores = vector.map((value, index) => Math.abs(value - Number(medianVector[index] ?? 0)) / Math.max(0.01, Number(madVector[index] ?? 0) * 1.4826))
  return median(scores)
}

export function euclideanVectorDistance(left: number[], right: number[]): number {
  if (!left.length || left.length !== right.length) return Number.POSITIVE_INFINITY
  return Math.sqrt(left.reduce((sum, value, index) => sum + ((value - Number(right[index] ?? 0)) ** 2), 0) / left.length)
}

export function median(values: number[]): number {
  return quantile(values, 0.5)
}

export function quantile(values: number[], ratio: number): number {
  const sorted = values.filter(Number.isFinite).sort((a, b) => a - b)
  if (!sorted.length) return 0
  const position = Math.max(0, Math.min(1, ratio)) * (sorted.length - 1)
  const lower = Math.floor(position)
  const upper = Math.ceil(position)
  const weight = position - lower
  return Number(sorted[lower]) * (1 - weight) + Number(sorted[upper]) * weight
}

function vectorSummary(vectors: number[][], width: number): Omit<RobustVectorSummary, 'retainedCount' | 'excludedCount'> {
  const medians = Array.from({ length: width }, (_, index) => median(vectors.map((vector) => Number(vector[index] ?? 0))))
  return {
    median: medians,
    mad: Array.from({ length: width }, (_, index) => median(vectors.map((vector) => Math.abs(Number(vector[index] ?? 0) - Number(medians[index] ?? 0))))),
    q10: Array.from({ length: width }, (_, index) => quantile(vectors.map((vector) => Number(vector[index] ?? 0)), 0.1)),
    q90: Array.from({ length: width }, (_, index) => quantile(vectors.map((vector) => Number(vector[index] ?? 0)), 0.9))
  }
}
