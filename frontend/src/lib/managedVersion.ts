interface UpdateMetadata {
  latest_version: string
  has_update: boolean
  mode: string
  check_status?: string
}

export function updateState(info: UpdateMetadata): { latestVersion: string | null; hasUpdate: boolean } {
  if (info.check_status === 'unknown') return { latestVersion: null, hasUpdate: false }
  const raw = info.latest_version.trim()
  const label = !raw ? null : info.mode === 'source_image' || /^[vV]/.test(raw) ? raw : 'v' + raw
  return { latestVersion: label, hasUpdate: info.has_update === true }
}
