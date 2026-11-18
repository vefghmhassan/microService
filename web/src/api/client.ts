// Thin fetch wrapper for /api/v1. The session is an HttpOnly cookie; the
// X-Requested-With header is what the server checks against CSRF.

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

let onUnauthorized: () => void = () => {}
export function setUnauthorizedHandler(f: () => void) {
  onUnauthorized = f
}

export async function api<T = unknown>(path: string, init: { method?: string; body?: unknown } = {}): Promise<T> {
  const res = await fetch('/api/v1' + path, {
    method: init.method ?? 'GET',
    credentials: 'same-origin',
    headers: {
      Accept: 'application/json',
      'X-Requested-With': 'microserver',
      ...(init.body !== undefined ? { 'Content-Type': 'application/json' } : {}),
    },
    body: init.body !== undefined ? JSON.stringify(init.body) : undefined,
  })
  const text = await res.text()
  let data: unknown = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = text
    }
  }
  if (!res.ok) {
    if (res.status === 401 && path !== '/auth/login') onUnauthorized()
    const msg = (data && typeof data === 'object' && 'error' in data ? String((data as { error: unknown }).error) : '') || res.statusText
    throw new ApiError(res.status, msg)
  }
  return data as T
}

export const get = <T,>(p: string) => api<T>(p)
export const post = <T,>(p: string, body?: unknown) => api<T>(p, { method: 'POST', body: body ?? {} })
export const put = <T,>(p: string, body?: unknown) => api<T>(p, { method: 'PUT', body: body ?? {} })
export const del = <T,>(p: string) => api<T>(p, { method: 'DELETE' })

async function errorOf(res: Response): Promise<ApiError> {
  if (res.status === 401) onUnauthorized()
  let msg = res.statusText
  try {
    const d = await res.json()
    if (d && typeof d === 'object' && 'error' in d) msg = String(d.error)
  } catch { /* not JSON */ }
  return new ApiError(res.status, msg)
}

/** postBlob POSTs JSON and returns the response as a file (name from Content-Disposition). */
export async function postBlob(path: string, body: unknown): Promise<{ blob: Blob; name: string }> {
  const res = await fetch('/api/v1' + path, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'X-Requested-With': 'microserver', 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) throw await errorOf(res)
  const m = /filename="?([^";]+)"?/.exec(res.headers.get('Content-Disposition') ?? '')
  return { blob: await res.blob(), name: m?.[1] ?? 'backup.msbk' }
}

/** postForm POSTs multipart form data and returns the JSON response. */
export async function postForm<T>(path: string, form: FormData): Promise<T> {
  const res = await fetch('/api/v1' + path, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { Accept: 'application/json', 'X-Requested-With': 'microserver' },
    body: form,
  })
  if (!res.ok) throw await errorOf(res)
  return res.json() as Promise<T>
}
