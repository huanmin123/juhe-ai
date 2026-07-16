export const maxChatImageCount = 5
export const maxChatImageBytes = 32 * 1024 * 1024

export function selectChatImageFileSlots(files: readonly File[], existingCount: number): Array<File | undefined> {
  let remaining = Math.max(0, maxChatImageCount - Math.max(0, existingCount))
  return files.map((file) => {
    if (!file.type.startsWith('image/') || file.size > maxChatImageBytes || remaining === 0) return undefined
    remaining -= 1
    return file
  })
}

export function selectChatImageFiles(files: readonly File[], existingCount: number): File[] {
  return selectChatImageFileSlots(files, existingCount).filter((file): file is File => Boolean(file))
}
