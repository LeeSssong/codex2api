import { useSyncExternalStore } from 'react'

let offset = 0
let now = Date.now() / 1000
let timer: ReturnType<typeof setInterval> | undefined
const listeners = new Set<() => void>()
const tick = () => {
  now = Date.now() / 1000 + offset
  listeners.forEach(listener => listener())
}

export function syncStateClock(serverTime?: number) {
  if (serverTime) offset = serverTime - Date.now() / 1000
  tick()
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  if (!timer) timer = setInterval(() => { if (!document.hidden) tick() }, 1000)
  return () => {
    listeners.delete(listener)
    if (!listeners.size) { clearInterval(timer); timer = undefined }
  }
}

export function useStateClock() {
  return useSyncExternalStore(subscribe, () => now)
}
