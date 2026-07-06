export const AUTH_STORAGE_KEY = 'serverstatus:admin-token'

export function getStoredAuthToken() {
  try {
    return localStorage.getItem(AUTH_STORAGE_KEY) || ''
  } catch {
    return ''
  }
}

export function setStoredAuthToken(token: string) {
  try {
    if (token) {
      localStorage.setItem(AUTH_STORAGE_KEY, token)
    } else {
      localStorage.removeItem(AUTH_STORAGE_KEY)
    }
  } catch {
    // Authorization storage is a browser convenience; API requests still work with cookies.
  }
}

export function clearStoredAuthToken() {
  setStoredAuthToken('')
}

export function authHeaders(init?: HeadersInit) {
  const headers = new Headers(init)
  const token = getStoredAuthToken()
  if (token) headers.set('Authorization', token)
  return headers
}
