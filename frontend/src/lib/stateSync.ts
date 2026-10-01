const eventName = 'codex2api:state-changed'

export function notifyStateChange<T>(value: T): T {
  window.dispatchEvent(new Event(eventName))
  try { localStorage.setItem(eventName, String(Date.now())) } catch { /* In-memory refresh still works. */ }
  return value
}

export function subscribeStateChange(refresh: () => void): () => void {
  const storage = (event: StorageEvent) => { if (event.key === eventName) refresh() }
  window.addEventListener(eventName, refresh)
  window.addEventListener('storage', storage)
  return () => {
    window.removeEventListener(eventName, refresh)
    window.removeEventListener('storage', storage)
  }
}
