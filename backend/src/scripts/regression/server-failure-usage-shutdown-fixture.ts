import {
  getPendingGatewayFailureUsageFinalizationCount,
  trackGatewayFailureUsageFinalization,
  waitForGatewayFailureUsageFinalizationsIdle
} from '../../modules/gateway/usage/failure-finalization.service.js'

const delayedFinalization = new Promise<void>((resolvePromise) => {
  setTimeout(resolvePromise, 120)
})
trackGatewayFailureUsageFinalization(delayedFinalization)

let shutdownStarted = false
const handleSignal = async (): Promise<void> => {
  if (shutdownStarted) return
  shutdownStarted = true
  const idle = await waitForGatewayFailureUsageFinalizationsIdle(2_000)
  process.stdout.write(`DRAINED idle=${idle} pending=${getPendingGatewayFailureUsageFinalizationCount()}\n`)
  process.exit(idle ? 0 : 1)
}

process.once('SIGTERM', () => void handleSignal())
process.on('message', (message) => {
  if (message === 'simulate-sigterm') {
    process.emit('SIGTERM')
  }
})
process.stdout.write(`READY pending=${getPendingGatewayFailureUsageFinalizationCount()}\n`)
