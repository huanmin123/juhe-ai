export function headersToObject(headers: Headers): Record<string, string> {
  const output: Record<string, string> = {}
  headers.forEach((value, name) => {
    output[name] = value
  })
  return output
}
