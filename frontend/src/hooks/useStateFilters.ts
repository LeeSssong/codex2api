import { useCallback } from 'react'
import { useSearchParams } from 'react-router-dom'

export function useStateFilters() {
  const [params, setParams] = useSearchParams()
  const raw = params.get('state')
  const state: 'valid' | 'available' | 'missing' | 'all' = raw === 'valid' || raw === 'available' || raw === 'missing' ? raw : 'all'
  const model = params.get('state_model') || ''
  const update = useCallback((values: { state?: string; model?: string }) => {
    setParams(current => {
      const next = new URLSearchParams(current)
      for (const [key, value] of Object.entries(values)) {
        const name = key === 'model' ? 'state_model' : 'state'
        if (!value || value === 'all') next.delete(name)
        else next.set(name, value)
      }
      return next
    }, { replace: true })
  }, [setParams])
  return { state, model, update }
}
