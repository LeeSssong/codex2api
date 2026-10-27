import { useEffect, useMemo } from 'react'
import { api } from '../api'
import type { AccountLiveStateResponse } from '../types'
import { mergeAccountLiveState } from '../lib/accountLiveState'

export { mergeAccountLiveState }

export function useAccountLiveState(
  ids: number[],
  apply: (response: AccountLiveStateResponse) => void,
  enabled = true,
) {
  const idsKey = useMemo(() => ids.join(','), [ids])

  useEffect(() => {
    if (!enabled) return undefined
    let stopped = false
    let timer: number | undefined
    let controller: AbortController | undefined
    let generation = 0

    const poll = async () => {
      if (stopped) return
      if (timer !== undefined) window.clearTimeout(timer)
      const version = ++generation
      if (document.hidden) {
        timer = window.setTimeout(poll, 30000)
        return
      }
      controller?.abort()
      const current = new AbortController()
      controller = current
      // A hung fetch must not stop the poll forever; abort it and let the next
      // tick retry. The list reload remains the fallback for stale data.
      const timeout = window.setTimeout(() => current.abort(), 5000)
      try {
        const response = await api.getAccountLiveState(
          idsKey.split(',').map(Number),
          current.signal,
        )
        if (!stopped && !current.signal.aborted) apply(response)
      } catch {
        // The next tick retries; live counters must never disrupt the page.
      } finally {
        window.clearTimeout(timeout)
      }
      if (!stopped && version === generation) timer = window.setTimeout(poll, 1000)
    }

    const refresh = () => { if (!document.hidden) void poll() }
    const unsubscribe = subscribeStateChange(refresh)
    document.addEventListener('visibilitychange', refresh)
    void poll()
    return () => {
      stopped = true
      unsubscribe()
      document.removeEventListener('visibilitychange', refresh)
      controller?.abort()
      if (timer !== undefined) window.clearTimeout(timer)
    }
  }, [apply, enabled, idsKey])
}
