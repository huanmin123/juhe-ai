export interface ChatCodeCopyButtonState {
  textContent: string | null
  readonly isConnected: boolean
}

type ScheduleReset = (callback: () => void, delayMilliseconds: number) => number
type CancelReset = (timer: number) => void

export class ChatCodeCopyResetController {
  private readonly timers = new Map<ChatCodeCopyButtonState, number>()

  constructor(
    private readonly schedule: ScheduleReset,
    private readonly cancel: CancelReset,
    private readonly delayMilliseconds = 1200
  ) {}

  markCopied(button: ChatCodeCopyButtonState): void {
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
    for (const timer of this.timers.values()) this.cancel(timer)
    this.timers.clear()
  }
}
