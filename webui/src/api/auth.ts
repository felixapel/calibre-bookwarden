let apiKey = ''
let unauthorized = false
let authRevision = 0
const listeners = new Set<() => void>()

export const getApiKey = () => apiKey
export const getUnauthorized = () => unauthorized
export const getAuthRevision = () => authRevision
export const subscribeAuth = (listener: () => void) => {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

const notify = () => listeners.forEach((listener) => listener())

export const markUnauthorized = () => {
  if (unauthorized) return
  unauthorized = true
  authRevision += 1
  notify()
}

export const setApiKey = (value: string) => {
  apiKey = value.trim()
  unauthorized = false
  authRevision += 1
  notify()
  window.dispatchEvent(new Event('bookaudit-authenticated'))
}
