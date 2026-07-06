type ApiWindow = Window & { SERVERSTATUS_API_BASE?: string }

function normalizeApiBase(input: string | undefined) {
  const value = String(input || '/api/v1').trim() || '/api/v1'
  return value.length > 1 ? value.replace(/\/+$/, '') : value
}

export const API_BASE = normalizeApiBase((window as ApiWindow).SERVERSTATUS_API_BASE)
export const ADMIN_API_BASE = `${API_BASE}/admin`
export const AUTH_API_BASE = `${API_BASE}/auth`

function joinPath(base: string, path = '') {
  if (!path) return base
  return `${base}${path.startsWith('/') ? path : `/${path}`}`
}

export function apiPath(path = '') {
  return joinPath(API_BASE, path)
}

export function adminApiPath(path = '') {
  return joinPath(ADMIN_API_BASE, path)
}

export function authApiPath(path = '') {
  return joinPath(AUTH_API_BASE, path)
}

export function apiWebSocketUrl(path = '/ws') {
  const url = new URL(apiPath(path), window.location.href)
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
  return url.toString()
}
