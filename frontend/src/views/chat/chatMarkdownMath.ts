export function normalizeChatMarkdownMathDelimiters(markdown: string): string {
  const parts = markdown.split(/(\r\n|\n|\r)/u)
  let fenceCharacter: '`' | '~' | undefined
  let fenceLength = 0
  let displayMathOpen = false

  for (let index = 0; index < parts.length; index += 2) {
    const line = parts[index] ?? ''
    const fence = line.match(/^\s*(`{3,}|~{3,})/u)?.[1]
    if (fence) {
      const character = fence[0] as '`' | '~'
      if (!fenceCharacter) {
        fenceCharacter = character
        fenceLength = fence.length
      } else if (character === fenceCharacter && fence.length >= fenceLength) {
        fenceCharacter = undefined
        fenceLength = 0
      }
      continue
    }
    if (!fenceCharacter) {
      const normalized = normalizeLineDelimiters(line)
      parts[index] = normalized
      if (countUnescapedDisplayDelimiters(normalized) % 2 === 1) displayMathOpen = !displayMathOpen
      if (displayMathOpen && index + 1 < parts.length) parts[index + 1] = ' '
    }
  }
  return parts.join('')
}

function countUnescapedDisplayDelimiters(value: string): number {
  let count = 0
  for (let index = 0; index + 1 < value.length; index += 1) {
    if (value[index] !== '$' || value[index + 1] !== '$') continue
    let backslashes = 0
    for (let cursor = index - 1; cursor >= 0 && value[cursor] === '\\'; cursor -= 1) backslashes += 1
    if (backslashes % 2 === 0) count += 1
    index += 1
  }
  return count
}

function normalizeLineDelimiters(value: string): string {
  let output = ''
  for (let index = 0; index < value.length; index += 1) {
    const character = value[index]!
    if (character !== '\\' || index + 1 >= value.length) {
      output += character
      continue
    }
    const next = value[index + 1]!
    if (next === '\\') {
      output += '\\\\'
      index += 1
      continue
    }
    if (next === '[' || next === ']') {
      output += '$$'
      index += 1
      continue
    }
    if (next === '(' || next === ')') {
      output += '$'
      index += 1
      continue
    }
    output += character
  }
  return output
}
