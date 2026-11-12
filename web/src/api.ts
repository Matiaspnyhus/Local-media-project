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
export interface UpdateStatus {
  supported: boolean
  whyNot?: string
  current: string
  available?: string
  checking: boolean
  installing: boolean
  problem?: string
  auto: boolean
  canUndo: boolean
  lastFrom?: string
  lastTo?: string
  lastResult?: string
  lastProblem?: string
}
export interface Folders {
  path: string
  parent: string
  dirs: { name: string; path: string }[]
}
export interface AudioTrack {
  index: number
  label: string
  language: string
  selected: boolean
}
export interface SubtitleTrack {
  key: string
  label: string
  language: string
  forced: boolean
  selected: boolean
  url: string
}
export interface PlayInfo {
  title: string
  url: string
  mimeType: string
  container: string
  positionMs: number
  durationMs: number
  // Set when the film reaches the player as a list of chunks (repackaged,
  // perhaps with its sound converted) rather than as the original file.
  hls?: string
  // The name this play goes by on the PC: for reporting trouble in it, for
  // asking how it is doing, for closing it, and for telling the PC which
  // play a new one replaces. The same as hls for a film sent in chunks; ''
  // for a film that cannot be played.
  play: string
  // The setting to ask for (lower) when the viewer wants a lighter picture
  // than this one; 0 when none is made.
  lighter: number
  // How far this player's clock runs ahead of the film's own time, in
  // milliseconds: 0 for nearly every film. positionMs is on the player's
  // clock already.
  clockAheadMs: number
  // The film's picture is HDR and is sent as it is, so its colours are for
  // the device to get right.
  hdr: boolean
  explain: string
  plan: PlayPlan
  audio: AudioTrack[]
  subtitles: SubtitleTrack[]
  pictureSubtitles: number
}
// How a film reaches this device, as the PC decided it. The PC sends
// exactly these fields (a test on the PC's side holds the two lists
// together).
export interface PlayPlan {
  playable: 'yes' | 'no' | 'unknown'
  // direct: the original file. remux: repackaged, nothing converted.
  // audio: repackaged with the sound converted. convert: the picture is
  // converted too. none: it cannot be played here.
  method: 'direct' | 'remux' | 'audio' | 'convert' | 'none'
  // Why the original file is not simply sent.
  reasons: string[]
  // null: the file has not been inspected, so nobody knows yet.
  hasAudio: boolean | null
  audioTrack: number
  audioCopy: boolean
  deviceUnknown: boolean
  // The answer is no only because this PC converts slower than the film plays.
  tooSlow: boolean
  // The answer is no because of this PC, not this device.
  pcCannot: boolean
  // What the picture is converted to; only meaningful for method convert.
  video: PlayVideo
  subtitles: 'none' | 'track' | 'draw' | 'burn'
  subtitleKind: '' | 'text' | 'styled' | 'picture'
  // What the viewer does not get compared with the original, in plain words.
  lost: string[]
  // The kind of picture this device is asked to show.
  picture: { codec: string; bitDepth: number; size: string }
}
export interface PlayVideo {
  rung: number // the setting, 1 (best picture) to 5 (lightest)
  height: number
  encoder: string
  hdrToSdr: boolean
  class: string
}
export interface Prefs {
  audioLang: string
  subLang: string
  subs: 'auto' | 'always' | 'off'
  // Subtitles that have a design of their own: shown as designed, or as
  // plain text. Kept by the PC; nothing on the page sets it yet.
  styled: 'designed' | 'plain'
}
// What to ask for when a film is opened, beyond the film itself.
export interface PlayWish {
  audio?: number // the sound track the viewer picked
  // A lighter picture: the number of the best setting that may be used
  // (see PlayVideo.rung and PlayInfo.lighter).
  lower?: number
  // The play this one replaces (PlayInfo.play): the same film started again
  // with another sound track, a lighter picture, or after trouble. The new
  // play then carries on what the old one had come to.
  from?: string
}
// What the PC answers to a report of trouble in a film that is playing.
export interface TroubleAnswer {
  // replay: start the film again where it is, naming this play as "from".
  // wait: leave it playing.
  action: 'replay' | 'wait'
  notice: string // one sentence for the viewer; '' when there is nothing to say
  lower: number // for information only; it is not sent back
  // What the report taught about this device. Forgetting these and playing
  // again without "from" undoes the step.
  lessons: string[]
}
// How a film the page has open is doing on the PC.
export interface PlayState {
  alive: boolean // false: the PC no longer has this film open, so it must be asked for again
  method: string
  rung: number
  pace: number // seconds of film the PC converts in a second; means nothing while chunks is under two
  chunks: number
  advice: '' | 'lower' // lower: the PC converts slower than the film plays, and a lighter picture would help
}
// One thing that was learned from trouble while playing, in the PC's words.
export interface Lesson {
  id: string
  kind: 'picture' | 'height' | 'rate' | 'hdr' | 'draw' | 'speed'
  text: string
  learnedAt: string // both as written by the PC: 2026-11-12T20:26:26Z
  expiresAt: string
}
// A device as the PC knows it: by kind ("iPhone", "Windows PC, Chrome"),
// never by a name.
export interface Device {
  label: string
  hdrWrong: boolean // the viewer said HDR colours look wrong on it
  lessons: Lesson[]
}
export interface KnownDevice extends Device {
  id: string
  lastSeenAt: string
  thisDevice?: boolean
}
// Every device, and what was learned about this PC's own speed.
export interface Devices {
  devices: KnownDevice[]
  pc: { lessons: Lesson[] }
}
export interface Diagnostics {
  device: Caps | null
  inspector: boolean
  converter: boolean
  running: number
  open: number
  userAgent: string
}
// What the PC gives a film's way out to another player (see OpenElsewhere).
export interface Handoff {
  // A complete address for the film's original file. It plays without
  // signing in, so a video player outside the browser can use it.
  url: string
  expiresAt: number // when the link stops working, in Unix seconds
  validSeconds: number // how long a link lasts from the moment it is made
  thisPC: boolean // this browser is on the PC that runs Local Media
}
// vlcFile is the address of a small playlist file for VLC that plays one
// film, starting at the given second.
export function vlcFile(id: string, atSeconds: number): string {
  return `/api/items/${id}/handoff.xspf` + (atSeconds > 0 ? `?at=${Math.floor(atSeconds)}` : '')
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
  hls: boolean
  // Which of the picture formats this device plays smoothly when the
  // picture is larger than 1080p (4K). Left out when the browser has no
  // way to ask; the PC then takes the device at its word for any size.
  tall?: Record<string, boolean>
  // The page itself can fill the screen (not so on an iPhone, where only
  // the video can).
  pageFullscreen?: boolean
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
  update: () => call<UpdateStatus>('GET', '/api/update'),
  updateCheck: () => call<UpdateStatus>('POST', '/api/update/check'),
  updateInstall: () => call<{ ok: boolean }>('POST', '/api/update/install'),
  updateUndo: () => call<{ ok: boolean }>('POST', '/api/update/undo'),
  setAutoUpdate: (on: boolean) => call<{ ok: boolean }>('PUT', '/api/settings/auto-update', { on }),
  health: () => call<{ status: string; version: string }>('GET', '/health'),
  capabilities: (c: Caps) => call<{ ok: boolean }>('POST', '/api/capabilities', c),
  play: (id: string, wish: PlayWish = {}) =>
    call<PlayInfo>('POST', `/api/items/${id}/play`, {
      ...(wish.audio === undefined ? {} : { audio: wish.audio }),
      ...(wish.lower ? { lower: wish.lower } : {}),
      ...(wish.from ? { from: wish.from } : {}),
    }),
  // Tells the PC that a film it is sending has run into trouble here.
  // evidence says what kind (see stall.ts); detail is a few words for the
  // PC's log.
  trouble: (token: string, evidence: 'decode' | 'starved' | 'renderer', detail: string) =>
    call<TroubleAnswer>('POST', `/api/hls/${token}/trouble`, { evidence, detail }),
  playState: (token: string) => call<PlayState>('GET', `/api/hls/${token}/state`),
  // What was learned about this device, and about every device (the owner).
  device: () => call<Device>('GET', '/api/device'),
  forgetLesson: (lessonId: string) => call<{ ok: boolean }>('DELETE', `/api/device/lessons/${lessonId}`),
  // The viewer's word that HDR colours look wrong on this device, or that
  // they look right after all. lesson names what was kept ('' for off).
  hdrWrong: (on: boolean) => call<{ ok: boolean; on: boolean; lesson: string }>('POST', '/api/device/hdr-wrong', { on }),
  devices: () => call<Devices>('GET', '/api/devices'),
  // deviceId is a device's id from the list, or 'pc' for what was learned
  // about this PC's own speed.
  forgetDeviceLesson: (deviceId: string, lessonId: string) =>
    call<{ ok: boolean }>('DELETE', `/api/devices/${deviceId}/lessons/${encodeURIComponent(lessonId)}`),
  // What the viewer picked while watching. The tracks themselves are sent
  // (audioIndex, -1 for none; subKey, '' for off) as well as their
  // languages: the same film is opened again with exactly those tracks,
  // and the languages carry the choice to the next episode.
  choice: (id: string, c: { audioLang: string; subLang: string; subsOn: boolean; audioIndex: number; subKey: string }) =>
    call<{ ok: boolean }>('PUT', `/api/items/${id}/choice`, c).catch(() => undefined),
  // Tells the PC to stop converting at once. Sent in a way that still
  // goes out while the page is being closed.
  hlsStop: (token: string) => {
    fetch(`/api/hls/${token}/stop`, {
      method: 'POST',
      headers: { 'X-Requested-With': 'localmedia', 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: '{}',
      keepalive: true,
    }).catch(() => undefined)
  },
  handoff: (id: string) => call<Handoff>('GET', `/api/items/${id}/handoff`),
  // Opens the film in the video player of the PC that runs Local Media.
  // The PC only does this for the browser on the PC itself.
  openLocal: (id: string) => call<{ ok: boolean }>('POST', `/api/items/${id}/open-local`),
  prefs: () => call<Prefs>('GET', '/api/prefs'),
  setPrefs: (p: Prefs) => call<Prefs>('PUT', '/api/prefs', p),
  diagnostics: () => call<Diagnostics>('GET', '/api/diagnostics'),
  progress: (id: string, positionMs: number, durationMs: number) =>
    call<{ ok: boolean }>('PUT', `/api/items/${id}/progress`, { positionMs, durationMs }),
  connect: () => call<{ urls: string[] }>('GET', '/api/connect'),
  scan: () => call<{ scan: Scan }>('POST', '/api/libraries/scan'),
  clientEvent: (kind: string, itemId: string, detail: string) =>
    call<{ ok: boolean }>('POST', '/api/client-events', { kind, itemId, detail }).catch(() => undefined),
}
