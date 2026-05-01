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
