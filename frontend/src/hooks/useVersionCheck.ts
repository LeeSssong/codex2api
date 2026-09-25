import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api'
import type { SystemUpdateInfo } from '../types'
import { updateState } from '../lib/managedVersion'

const CACHE_KEY = 'codex2api_source_update_v2'
const CACHE_TTL = 10 * 60 * 1000
const POLL_INTERVAL = 30 * 60 * 1000

function cachedInfo(): SystemUpdateInfo | null {
  try {
    const cached = JSON.parse(localStorage.getItem(CACHE_KEY) || 'null')
    if (!cached || cached.build !== __APP_VERSION__ || typeof cached.checkedAt !== 'number' || Date.now() - cached.checkedAt >= CACHE_TTL) return null
    if (typeof cached.info?.has_update !== 'boolean' || typeof cached.info?.latest_version !== 'string') return null
    return cached.info
  } catch { return null }
}

async function fetchUpdateInfo(forceNetwork: boolean): Promise<SystemUpdateInfo | null> {
  if (!forceNetwork) {
    const cached = cachedInfo()
    if (cached) return cached
  }
  try {
    const info = await api.getSystemUpdate()
    try {
      localStorage.removeItem('codex2api_latest_release')
      localStorage.removeItem('codex2api_latest_version')
      if ((info as SystemUpdateInfo & {check_status?: string}).check_status !== 'unknown') {
        localStorage.setItem(CACHE_KEY, JSON.stringify({build:__APP_VERSION__,checkedAt:Date.now(),info}))
      } else localStorage.removeItem(CACHE_KEY)
    } catch { /* private browsing can disable storage */ }
    return info
  } catch { return null }
}

export function useVersionCheck(triggerKey?: string) {
  const [updateInfo, setUpdateInfo] = useState<SystemUpdateInfo | null>(null)
  const [latestVersion, setLatestVersion] = useState<string | null>(null)
  const [hasUpdate, setHasUpdate] = useState(false)
  const lastTriggerRef = useRef<string | undefined>(undefined)
  const check = useCallback(async (forceNetwork = false) => {
    if (__APP_VERSION__ === 'dev') return
    const info = await fetchUpdateInfo(forceNetwork)
    setUpdateInfo(info)
    const state = info ? updateState(info) : {latestVersion:null,hasUpdate:false}
    setLatestVersion(state.latestVersion)
    setHasUpdate(state.hasUpdate)
  }, [])
  useEffect(() => {
    void check()
    const timer = setInterval(() => void check(), POLL_INTERVAL)
    return () => clearInterval(timer)
  }, [check])
  useEffect(() => {
    if (triggerKey === undefined) return
    if (lastTriggerRef.current === undefined) {lastTriggerRef.current = triggerKey;return}
    if (lastTriggerRef.current === triggerKey) return
    lastTriggerRef.current = triggerKey
    void check(true)
  }, [check, triggerKey])
  return {hasUpdate,latestVersion,updateInfo,refreshVersion:check}
}
