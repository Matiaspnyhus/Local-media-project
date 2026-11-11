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
  kind: string
  title: string
  year?: number
  showId?: string
  showTitle?: string
  season: number
  episode: number
  episodeEnd?: number
  container: string
  durationMs: number
  positionMs: number
  played: boolean
  playable: 'yes' | 'no' | 'unknown'
  reasons: string[]
  overview?: string
  poster: boolean
}
export interface Show {
  id: string
  title: string
  year?: number
  episodes: number
  watched: number
  overview?: string
  poster: boolean
}
export interface Season {
  number: number
  title: string
  episodes: Item[]
}
export interface Home {
  continue: Item[]
  nextUp: Item[]
  counts: { movies: number; shows: number }
  inspection: { available: boolean; pending: number; unreadable: number; running: boolean }
  lookups: { on: boolean; pending: number; running: boolean }
}
export interface Settings {
  tmdbKeySaved: boolean
  online: boolean
  lookups: { pending: number; found: number; notFound: number; running: boolean; problem: string }
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
  plan: { direct: 'yes' | 'no' | 'unknown'; reasons: string[]; hasAudio: boolean | null }
}
export interface Scan {
  added: number
  moved: number
  missing: number
  total: number
}
export interface Caps {
  containers: Record<string, boolean>
  video: Record<string, boolean>
  audio: Record<string, boolean>
}

export const api = {
  state: () => call<State>('GET', '/api/state'),
  setupFolders: (path: string) => call<Folders>('GET', '/api/setup/folders?path=' + encodeURIComponent(path)),
  setup: (name: string, password: string, folder: string) =>
    call<{ scan: Scan }>('POST', '/api/setup', { name, password, folder }),
  login: (name: string, password: string) => call<{ ok: boolean }>('POST', '/api/login', { name, password }),
  logout: () => call<{ ok: boolean }>('POST', '/api/logout'),
  home: () => call<Home>('GET', '/api/home'),
  movies: () => call<{ items: Item[] }>('GET', '/api/movies'),
  shows: () => call<{ shows: Show[] }>('GET', '/api/shows'),
  show: (id: string) => call<{ show: Show; seasons: Season[] }>('GET', `/api/shows/${id}`),
  search: (q: string) => call<{ items: Item[]; shows: Show[] }>('GET', '/api/search?q=' + encodeURIComponent(q)),
  settings: () => call<Settings>('GET', '/api/settings'),
  setTmdbKey: (key: string) => call<{ ok: boolean }>('PUT', '/api/settings/tmdb', { key }),
  setOnline: (on: boolean) => call<{ ok: boolean }>('PUT', '/api/settings/online', { on }),
  capabilities: (c: Caps) => call<{ ok: boolean }>('POST', '/api/capabilities', c),
  play: (id: string) => call<PlayInfo>('POST', `/api/items/${id}/play`),
  progress: (id: string, positionMs: number, durationMs: number) =>
    call<{ ok: boolean }>('PUT', `/api/items/${id}/progress`, { positionMs, durationMs }),
  connect: () => call<{ urls: string[] }>('GET', '/api/connect'),
  scan: () => call<{ scan: Scan }>('POST', '/api/libraries/scan'),
  clientEvent: (kind: string, itemId: string, detail: string) =>
    call<{ ok: boolean }>('POST', '/api/client-events', { kind, itemId, detail }).catch(() => undefined),
}
