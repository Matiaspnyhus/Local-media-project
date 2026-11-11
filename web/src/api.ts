// The one place the page talks to the server.

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  if (method !== 'GET') {
    headers['X-Requested-With'] = 'localmedia'
    headers['Content-Type'] = 'application/json'
  }
  let res: Response
  try {
    res = await fetch(path, {
      method,
      headers,
      credentials: 'same-origin',
      body: method === 'GET' ? undefined : JSON.stringify(body ?? {}),
    })
  } catch {
    throw new ApiError(0, 'Cannot reach Local Media. Is the PC on and the program running?')
  }
  let data: any = null
  try {
    data = await res.json()
  } catch {
    /* not JSON */
  }
  if (!res.ok) throw new ApiError(res.status, (data && data.error) || `Something went wrong (${res.status}).`)
  return data as T
}

export interface State {
  setupDone: boolean
  version: string
  user: { name: string; role: string } | null
}
export interface Item {
  id: string
  title: string
  container: string
  folder: string
  positionMs: number
  durationMs: number
  played: boolean
}
export interface Folders {
  path: string
  parent: string
  dirs: { name: string; path: string }[]
}
export interface PlayInfo {
  title: string
  url: string
  mimeType: string
  container: string
  positionMs: number
}
export interface Scan {
  added: number
  missing: number
  total: number
}

export const api = {
  state: () => call<State>('GET', '/api/state'),
  setupFolders: (path: string) => call<Folders>('GET', '/api/setup/folders?path=' + encodeURIComponent(path)),
  setup: (name: string, password: string, folder: string) =>
    call<{ scan: Scan }>('POST', '/api/setup', { name, password, folder }),
  login: (name: string, password: string) => call<{ ok: boolean }>('POST', '/api/login', { name, password }),
  logout: () => call<{ ok: boolean }>('POST', '/api/logout'),
  items: () => call<{ items: Item[] }>('GET', '/api/items'),
  play: (id: string) => call<PlayInfo>('POST', `/api/items/${id}/play`),
  progress: (id: string, positionMs: number, durationMs: number) =>
    call<{ ok: boolean }>('PUT', `/api/items/${id}/progress`, { positionMs, durationMs }),
  connect: () => call<{ urls: string[] }>('GET', '/api/connect'),
  scan: () => call<{ scan: Scan }>('POST', '/api/libraries/scan'),
  clientEvent: (kind: string, itemId: string, detail: string) =>
    call<{ ok: boolean }>('POST', '/api/client-events', { kind, itemId, detail }).catch(() => undefined),
}
