export interface ChatCodeCopyButtonState {
  textContent: string | null
  readonly isConnected: boolean
}

type ScheduleReset = (callback: () => void, delayMilliseconds: number) => number
type CancelReset = (timer: number) => void

export class ChatCodeCopyResetController {
  private readonly timers = new Map<ChatCodeCopyButtonState, number>()
  private disposed = false

  constructor(
    private readonly schedule: ScheduleReset,
    private readonly cancel: CancelReset,
    private readonly delayMilliseconds = 1200
  ) {}

  markCopied(button: ChatCodeCopyButtonState): void {
    if (this.disposed) return
    const previous = this.timers.get(button)
    if (previous !== undefined) this.cancel(previous)
    button.textContent = '已复制'
    const timer = this.schedule(() => {
      if (this.timers.get(button) !== timer) return
      this.timers.delete(button)
      if (button.isConnected) button.textContent = '复制'
    }, this.delayMilliseconds)
    this.timers.set(button, timer)
  }

  dispose(): void {
    if (this.disposed) return
    this.disposed = true
    for (const timer of this.timers.values()) this.cancel(timer)
    this.timers.clear()
  }
}

export class ChatCodeCopyLifecycle {
  private active = false
  private generation = 0

  constructor(private readonly resetController: ChatCodeCopyResetController) {}

  activate(): void {
    this.generation += 1
    this.active = true
  }

  async copy(
    button: ChatCodeCopyButtonState,
    content: string,
    writeClipboard: (value: string) => Promise<void>,
    isCurrentButton: () => boolean,
    notifyFailure: () => void
  ): Promise<void> {
    const generation = this.generation
    try {
      await writeClipboard(content)
    } catch {
      if (this.isCurrent(generation, isCurrentButton)) notifyFailure()
      return
    }
    if (!this.isCurrent(generation, isCurrentButton)) return
    this.resetController.markCopied(button)
  }

  dispose(): void {
    this.active = false
    this.generation += 1
    this.resetController.dispose()
  }

  private isCurrent(generation: number, isCurrentButton: () => boolean): boolean {
    return this.active && this.generation === generation && isCurrentButton()
  }
}
