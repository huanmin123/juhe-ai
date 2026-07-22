export function isCompleteMarkdownCodeFence(raw: string): boolean {
  const normalized = raw.replace(/\r\n?/g, '\n')
  const lines = normalized.split('\n')
  const opening = lines[0]?.match(/^ {0,3}(`{3,}|~{3,})[^\n]*$/)
  if (!opening) return true

  const fence = opening[1]!
  const marker = fence[0]!
  const closing = new RegExp(`^ {0,3}\\${marker}{${fence.length},}[ \\t]*$`)
  return lines.slice(1).some((line) => closing.test(line))
}
