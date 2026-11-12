import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { api, ApiError, type Folders, type Home, type Item, type Diagnostics, type PlayInfo, type Prefs, type Season, type Settings, type Show, type State, type UpdateStatus } from './api'
import { builtInChunks, measureCaps } from './caps'
import { Learned } from './Learned'
import { OpenElsewhere } from './OpenElsewhere'
import { RULES, lateChunk, look, mark, newWatch, reading, type Evidence, type Watch as StallWatch } from './stall'

type Screen = { name: 'library' } | { name: 'watch'; id: string } | { name: 'show'; id: string }

function screenFromLocation(): Screen {
  const m = window.location.pathname.match(/^\/(watch|show)\/([0-9a-f]{32})$/)
  return m ? { name: m[1] as 'watch' | 'show', id: m[2] } : { name: 'library' }
}

export function App() {
  const [state, setState] = useState<State | null>(null)
  const [error, setError] = useState('')
  const [screen, setScreen] = useState<Screen>(screenFromLocation)

  const refresh = useCallback(() => {
    api
      .state()
      .then((s) => {
        setState(s)
        setError('')
      })
      .catch((e: ApiError) => setError(e.message))
  }, [])

  useEffect(refresh, [refresh])
  useEffect(() => {
    const onPop = () => setScreen(screenFromLocation())
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])

  const go = (s: Screen) => {
    window.history.pushState(null, '', s.name === 'library' ? '/' : `/${s.name}/${s.id}`)
    setScreen(s)
  }
  const back = () => (window.history.length > 1 ? window.history.back() : go({ name: 'library' }))

  // Tell the server what this browser can play, once per visit, so it can
  // mark the files that will not play here.
  const signedIn = !!state?.user
  const [capsSent, setCapsSent] = useState(false)
  useEffect(() => {
    if (!signedIn) return setCapsSent(false)
    measureCaps()
      .then((caps) => api.capabilities(caps))
      .catch(() => undefined)
      .then(() => setCapsSent(true))
  }, [signedIn])

  if (error)
    return (
      <main>
        <div className="card narrow">
          <h1>Local Media</h1>
          <p className="error">{error}</p>
          <button onClick={refresh}>Try again</button>
        </div>
      </main>
    )
  if (!state)
    return (
      <main>
        <p className="dim">Loading…</p>
      </main>
    )
  if (!state.setupDone) return <Setup onDone={refresh} />
  if (!state.user) return <Login onDone={refresh} />
  if (!capsSent)
    return (
      <main>
        <p className="dim">Loading…</p>
      </main>
    )
  const play = (id: string) => go({ name: 'watch', id })
  if (screen.name === 'watch') return <Watch id={screen.id} onBack={back} onSignedOut={refresh} />
  if (screen.name === 'show') return <ShowPage id={screen.id} onBack={back} onPlay={play} onSignedOut={refresh} />
  return <Library state={state} onPlay={play} onShow={(id) => go({ name: 'show', id })} onSignedOut={refresh} />
}

function Setup({ onDone }: { onDone: () => void }) {
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [folder, setFolder] = useState('')
  const [listing, setListing] = useState<Folders | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const open = useCallback((path: string) => {
    api
      .setupFolders(path)
      .then((l) => {
        setListing(l)
        setFolder(l.path)
        setError('')
      })
      .catch((e: ApiError) => setError(e.message))
  }, [])
  useEffect(() => open(''), [open])

  const submit = (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    api
      .setup(name, password, folder)
      .then(onDone)
      .catch((err: ApiError) => {
        setError(err.message)
        setBusy(false)
      })
  }

  return (
    <main>
      <form className="card narrow" onSubmit={submit}>
        <h1>Set up Local Media</h1>
        <p className="dim">
          Two steps. Everything stays on this PC. Your video files are only read, never changed.
        </p>

        <h2>1. Create your sign-in</h2>
        <label htmlFor="name">User name</label>
        <input id="name" type="text" autoComplete="username" value={name} onChange={(e) => setName(e.target.value)} required />
        <label htmlFor="password">Password (at least 8 characters)</label>
        <input
          id="password"
          type="password"
          autoComplete="new-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          minLength={8}
          required
        />

        <h2>2. Pick the folder with your videos</h2>
        <p className="picked" data-testid="picked-folder">
          {folder || 'No folder chosen yet'}
        </p>
        <div className="folders" role="list" aria-label="Folders">
          {listing?.parent ? (
            <button type="button" onClick={() => open(listing.parent)}>
              ↑ Up one level
            </button>
          ) : listing?.path ? (
            <button type="button" onClick={() => open('')}>
              ↑ All drives and folders
            </button>
          ) : null}
          {listing?.dirs.map((d) => (
            <button type="button" key={d.path} onClick={() => open(d.path)}>
              📁 {d.name}
            </button>
          ))}
          {listing && listing.dirs.length === 0 && <p className="dim" style={{ padding: '0 1rem' }}>No folders inside this one.</p>}
        </div>
        <label htmlFor="folder">Or type the folder's path</label>
        <input id="folder" type="text" value={folder} onChange={(e) => setFolder(e.target.value)} />

        {error && <p className="error" role="alert">{error}</p>}
        <p>
          <button className="primary" type="submit" disabled={busy || !name || password.length < 8 || !folder}>
            {busy ? 'Setting up…' : 'Finish setup'}
          </button>
        </p>
      </form>
    </main>
  )
}

function Login({ onDone }: { onDone: () => void }) {
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    api
      .login(name, password)
      .then(onDone)
      .catch((err: ApiError) => {
        setError(err.message)
        setBusy(false)
      })
  }
  return (
    <main>
      <form className="card narrow" onSubmit={submit}>
        <h1>Sign in to Local Media</h1>
        <label htmlFor="name">User name</label>
        <input id="name" type="text" autoComplete="username" autoCapitalize="none" value={name} onChange={(e) => setName(e.target.value)} required />
        <label htmlFor="password">Password</label>
        <input id="password" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
        {error && <p className="error" role="alert">{error}</p>}
        <p>
          <button className="primary" type="submit" disabled={busy}>
            Sign in
          </button>
        </p>
      </form>
    </main>
  )
}

function formatTime(ms: number): string {
  const s = Math.floor(ms / 1000)
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  return (h ? h + ':' + String(m).padStart(2, '0') : String(m)) + ':' + String(sec).padStart(2, '0')
}

function containerLabel(c: string): string {
  const names: Record<string, string> = { matroska: 'MKV', mpegts: 'TS', asf: 'WMV', mov: 'MOV' }
  return names[c] || c.toUpperCase()
}

const NEEDS = 'Does not play on this device'

function itemLabel(it: Item, withShow: boolean): string {
  if (it.kind !== 'episode') return it.title + (it.year ? ` (${it.year})` : '')
  const num = it.episodeEnd ? `Episodes ${it.episode}–${it.episodeEnd}` : `Episode ${it.episode}`
  const ep = /^Episodes? \d/.test(it.title) ? num : `${it.episode}. ${it.title}`
  return withShow ? `${it.showTitle} · Season ${it.season} · ${num}` : ep
}

function Library({
  state,
  onPlay,
  onShow,
  onSignedOut,
}: {
  state: State
  onPlay: (id: string) => void
  onShow: (id: string) => void
  onSignedOut: () => void
}) {
  const [home, setHome] = useState<Home | null>(null)
  const [movies, setMovies] = useState<Item[] | null>(null)
  const [shows, setShows] = useState<Show[] | null>(null)
  const [tab, setTab] = useState<'films' | 'shows'>(() => (sessionStorageGet('tab') === 'shows' ? 'shows' : 'films'))
  const [query, setQuery] = useState('')
  const [found, setFound] = useState<{ items: Item[]; shows: Show[] } | null>(null)
  const [error, setError] = useState('')
  const [note, setNote] = useState('')
  const [urls, setUrls] = useState<string[]>([])
  const [showConnect, setShowConnect] = useState(false)
  const [showSettings, setShowSettings] = useState(false)
  const isOwner = state.user?.role === 'owner'

  const load = useCallback(() => {
    const fail = (e: ApiError) => (e.status === 401 ? onSignedOut() : setError(e.message))
    api.home().then(setHome).catch(fail)
    api.movies().then((r) => setMovies(r.items)).catch(fail)
    api.shows().then((r) => setShows(r.shows)).catch(fail)
  }, [onSignedOut])
  useEffect(load, [load])
  useEffect(() => {
    api.connect().then((r) => setUrls(r.urls)).catch(() => undefined)
  }, [])
  // While files are still being inspected, look again every few seconds.
  const pending = (home?.inspection.available ? home.inspection.pending : 0) + (home?.lookups.on ? home.lookups.pending : 0)
  useEffect(() => {
    if (!pending) return
    const t = window.setTimeout(load, 4000)
    return () => window.clearTimeout(t)
  }, [pending, home, load])

  useEffect(() => {
    const q = query.trim()
    if (!q) return setFound(null)
    const t = window.setTimeout(() => {
      api.search(q).then(setFound).catch(() => undefined)
    }, 200)
    return () => window.clearTimeout(t)
  }, [query])

  const rescan = () => {
    setNote('Looking for new files…')
    api
      .scan()
      .then((r) => {
        const moved = r.scan.moved ? `, ${r.scan.moved} moved or renamed` : ''
        setNote(`Found ${r.scan.total} video files (${r.scan.added} new${moved}).`)
        load()
      })
      .catch((e: ApiError) => setNote(e.message))
  }
  const signOut = () => api.logout().then(onSignedOut, onSignedOut)
  const pickTab = (t: 'films' | 'shows') => {
    setTab(t)
    sessionStorageSet('tab', t)
  }
  const empty = movies && shows && movies.length === 0 && shows.length === 0

  return (
    <main>
      <div className="bar">
        <h1>Local Media</h1>
        <button onClick={() => setShowConnect((v) => !v)} aria-expanded={showConnect}>
          Open on another device
        </button>
        {isOwner && <button onClick={rescan}>Look for new files</button>}
        {isOwner && (
          <a className="button" href="/api/support-bundle" download>
            Export support bundle
          </a>
        )}
        <button onClick={() => setShowSettings((v) => !v)} aria-expanded={showSettings}>
          Settings
        </button>
        <button onClick={signOut}>Sign out</button>
      </div>

      {showSettings && <PlaybackCard />}
      {showSettings && <Learned owner={isOwner} onSignedOut={onSignedOut} />}
      {showSettings && isOwner && <SettingsCard onChanged={load} />}
      {showSettings && isOwner && <UpdateCard />}
      {showConnect && (
        <div className="card" data-testid="connect">
          <h2 style={{ marginTop: 0 }}>Open on a phone, tablet or another PC</h2>
          {urls.length === 0 ? (
            <p className="note">No home network address was found. Is this PC connected to your Wi-Fi or network?</p>
          ) : (
            <div className="split">
              <img className="qr" src="/api/connect/qr.png?i=0" alt="QR code for the address below" />
              <div>
                <p>On the same Wi-Fi, scan the code with the phone's camera, or type this address into its browser:</p>
                {urls.map((u) => (
                  <p className="addr" key={u}>
                    {u}
                  </p>
                ))}
                <p className="dim">
                  Then sign in with the same user name and password. On an iPhone, the Share button in Safari has "Add to
                  Home Screen", which gives Local Media its own icon.
                </p>
              </div>
            </div>
          )}
        </div>
      )}

      {note && <p className="note" role="status">{note}</p>}
      {error && <p className="error" role="alert">{error}</p>}
      {home && home.inspection.available && home.inspection.pending > 0 && (
        <p className="dim" role="status">
          Inspecting your files: {home.inspection.pending} to go. Until a file has been inspected, the app cannot say
          whether this browser can play it.
        </p>
      )}
      {home && !home.inspection.available && (
        <p className="dim">
          This copy of Local Media has no file inspector beside it, so it cannot tell in advance which files this
          browser can play.
        </p>
      )}

      <label htmlFor="search" className="visually-hidden">
        Search
      </label>
      <input
        id="search"
        type="text"
        placeholder="Search films and shows"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        autoComplete="off"
      />

      {found ? (
        <section aria-label="Search results">
          <h2>
            {found.items.length + found.shows.length === 0 ? `Nothing found for "${query.trim()}"` : 'Search results'}
          </h2>
          <ShowList shows={found.shows} onShow={onShow} />
          <ItemList items={found.items} onPlay={onPlay} withShow />
        </section>
      ) : (
        <>
          {home && home.continue.length > 0 && (
            <section>
              <h2>Continue watching</h2>
              <ItemList items={home.continue} onPlay={onPlay} withShow />
            </section>
          )}
          {home && home.nextUp.length > 0 && (
            <section>
              <h2>Next up</h2>
              <ItemList items={home.nextUp} onPlay={onPlay} withShow />
            </section>
          )}
          {empty && (
            <div className="card">
              <p>No video files were found in your folder yet.</p>
              <p className="dim">Copy a video into the folder and press "Look for new files".</p>
            </div>
          )}
          {movies && shows && !empty && (
            <section>
              <div className="tabs" role="tablist">
                <button role="tab" aria-selected={tab === 'films'} onClick={() => pickTab('films')}>
                  Films ({movies.length})
                </button>
                <button role="tab" aria-selected={tab === 'shows'} onClick={() => pickTab('shows')}>
                  Shows ({shows.length})
                </button>
              </div>
              {tab === 'films' ? <ItemList items={movies} onPlay={onPlay} withShow posters /> : <ShowList shows={shows} onShow={onShow} />}
              {tab === 'films' && movies.length === 0 && <p className="dim">No films found.</p>}
              {tab === 'shows' && shows.length === 0 && <p className="dim">No shows found.</p>}
            </section>
          )}
        </>
      )}
      <p className="dim" style={{ marginTop: '2rem' }}>
        Local Media {state.version}. Files this device cannot play as they are get repackaged while you watch, with
        their sound or picture converted if need be. Files that cannot be played here even so are marked. Posters and
        descriptions come from TMDB: this product uses the TMDB API but is not endorsed or certified by TMDB.
      </p>
    </main>
  )
}

function sessionStorageGet(key: string): string | null {
  try {
    return window.sessionStorage.getItem('lm.' + key)
  } catch {
    return null
  }
}
function sessionStorageSet(key: string, value: string) {
  try {
    window.sessionStorage.setItem('lm.' + key, value)
  } catch {
    /* private mode: the tab is simply not remembered */
  }
}

function ShowList({ shows, onShow }: { shows: Show[]; onShow: (id: string) => void }) {
  return (
    <ul className="list">
      {shows.map((sh) => (
        <li key={sh.id}>
          <button className="row withposter" onClick={() => onShow(sh.id)}>
            <Poster id={sh.id} has={sh.poster} />
            <div>
              <div className="title">
                {sh.title}
                {sh.year ? ` (${sh.year})` : ''}
              </div>
              <div className="meta">
                {sh.episodes} {sh.episodes === 1 ? 'episode' : 'episodes'}
                {sh.watched > 0 ? ` · ${sh.watched} watched` : ''}
              </div>
              {sh.overview && <div className="overview">{sh.overview}</div>}
            </div>
          </button>
        </li>
      ))}
    </ul>
  )
}

function Poster({ id, has }: { id: string; has: boolean }) {
  return has ? (
    <img className="poster" src={`/api/items/${id}/poster`} alt="" loading="lazy" />
  ) : (
    <div className="poster none" aria-hidden="true" />
  )
}

function SettingsCard({ onChanged }: { onChanged: () => void }) {
  const [st, setSt] = useState<Settings | null>(null)
  const [key, setKey] = useState('')
  const [msg, setMsg] = useState('')
  const [busy, setBusy] = useState(false)
  const load = useCallback(() => {
    api.settings().then(setSt).catch((e: ApiError) => setMsg(e.message))
  }, [])
  useEffect(load, [load])
  const working = !!st && st.tmdbKeySaved && st.online && (st.lookups.running || st.lookups.pending > 0) && !st.lookups.problem
  useEffect(() => {
    if (!working) return
    const t = window.setTimeout(() => {
      load()
      onChanged()
    }, 3000)
    return () => window.clearTimeout(t)
  }, [working, st, load, onChanged])

  const save = (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setMsg('')
    api
      .setTmdbKey(key)
      .then(() => {
        setKey('')
        setMsg('Key saved. Looking up your films and shows now.')
        load()
      })
      .catch((err: ApiError) => setMsg(err.message))
      .then(() => setBusy(false))
  }
  const toggle = (on: boolean) => api.setOnline(on).then(load, (e: ApiError) => setMsg(e.message))
  const remove = () => api.setTmdbKey('').then(load, (e: ApiError) => setMsg(e.message))

  return (
    <div className="card" data-testid="settings">
      <h2 style={{ marginTop: 0 }}>Posters and descriptions from the internet</h2>
      <p className="dim">
        They come from TMDB, a free film and TV database. Only titles and years are sent, never file names or anything
        else about your PC. Your key stays on this PC.
      </p>
      {st && st.tmdbKeySaved ? (
        <>
          <p>
            A TMDB key is saved. {st.lookups.found} found, {st.lookups.notFound} not found
            {st.lookups.pending > 0 ? `, ${st.lookups.pending} still to look up` : ''}.
          </p>
          {st.lookups.problem && <p className="error">Lookups stopped: {st.lookups.problem}.</p>}
          <p>
            <label className="check">
              <input type="checkbox" checked={st.online} onChange={(e) => toggle(e.target.checked)} /> Look up new films
              and shows online
            </label>
          </p>
          <button onClick={remove}>Remove the key</button>
        </>
      ) : (
        <form onSubmit={save}>
          <label htmlFor="tmdbkey">TMDB API key (the short one, 32 letters and digits)</label>
          <input id="tmdbkey" type="password" autoComplete="off" value={key} onChange={(e) => setKey(e.target.value)} />
          <p>
            <button className="primary" type="submit" disabled={busy || !key.trim()}>
              {busy ? 'Checking the key…' : 'Save key'}
            </button>
          </p>
        </form>
      )}
      {msg && <p className="note" role="status">{msg}</p>}
    </div>
  )
}

function UpdateCard() {
  const [st, setSt] = useState<UpdateStatus | null>(null)
  const [msg, setMsg] = useState('')
  const [waiting, setWaiting] = useState('') // the version we were on when a restart began
  const load = useCallback(() => {
    api.update().then(setSt).catch(() => undefined)
  }, [])
  useEffect(load, [load])
  // While the program restarts into another version, ask now and then
  // whether it is back, and reload the page when it is.
  useEffect(() => {
    if (!waiting) return
    const t = window.setInterval(() => {
      api
        .update()
        .then((now) => {
          if (now.current !== waiting || now.lastResult === 'failed') {
            if (now.current !== waiting) window.location.reload()
            else {
              setWaiting('')
              setSt(now)
              setMsg('')
            }
          }
        })
        .catch(() => undefined)
    }, 1500)
    return () => window.clearInterval(t)
  }, [waiting])

  if (!st) return null
  const check = () => {
    setMsg('Looking for a newer version…')
    api
      .updateCheck()
      .then((now) => {
        setSt(now)
        setMsg(now.available ? '' : now.problem ? '' : 'You have the newest version.')
      })
      .catch((e: ApiError) => setMsg(e.message))
  }
  const install = () => {
    setMsg('Installing. Local Media restarts by itself; this takes about a minute.')
    setWaiting(st.current)
    api.updateInstall().catch((e: ApiError) => {
      setMsg(e.message)
      setWaiting('')
    })
  }
  const undo = () => {
    setMsg('Going back. Local Media restarts by itself.')
    setWaiting(st.current)
    api.updateUndo().catch((e: ApiError) => {
      setMsg(e.message)
      setWaiting('')
    })
  }
  return (
    <div className="card" data-testid="updates">
      <h2 style={{ marginTop: 0 }}>Updates</h2>
      <p>You have version {st.current}.</p>
      {!st.supported ? (
        <p className="dim">{st.whyNot}</p>
      ) : (
        <>
          {st.available && (
            <p>
              Version {st.available} is available.{' '}
              <button className="primary" onClick={install} disabled={!!waiting}>
                Install it now
              </button>
            </p>
          )}
          {st.problem && <p className="note">{st.problem}</p>}
          {st.lastResult === 'failed' && <p className="error">{st.lastProblem} Nothing was lost.</p>}
          {st.lastResult === 'ok' && st.lastTo === st.current && (
            <p className="dim">
              Updated from {st.lastFrom}. Your library and what you have watched were backed up first.
            </p>
          )}
          <p>
            <label className="check">
              <input
                type="checkbox"
                checked={st.auto}
                onChange={(e) => api.setAutoUpdate(e.target.checked).then(load, () => undefined)}
              />{' '}
              Install new versions by themselves, when nobody is watching
            </label>
          </p>
          <div className="bar">
            <button onClick={check} disabled={!!waiting || st.checking}>
              Look for a newer version
            </button>
            {st.canUndo && (
              <button onClick={undo} disabled={!!waiting}>
                Go back to version {st.lastFrom}
              </button>
            )}
          </div>
        </>
      )}
      {msg && <p className="note" role="status">{msg}</p>}
    </div>
  )
}

function ItemList({
  items,
  onPlay,
  withShow,
  posters,
}: {
  items: Item[]
  onPlay: (id: string) => void
  withShow?: boolean
  posters?: boolean
}) {
  return (
    <ul className="list">
      {items.map((it) => (
        <li key={it.id}>
          <button className={posters ? 'row withposter' : 'row'} onClick={() => onPlay(it.id)}>
            {posters && <Poster id={it.id} has={it.poster} />}
            <div>
            <div className="title">{itemLabel(it, !!withShow)}</div>
            <div className="meta">
              {it.durationMs > 0 ? formatTime(it.durationMs) + ' · ' : ''}
              {containerLabel(it.container)}
              {it.played ? ' · Watched' : it.positionMs > 0 ? ` · Stopped at ${formatTime(it.positionMs)}` : ''}
            </div>
            {it.playable === 'no' && <div className="needs">{NEEDS}</div>}
            {posters && it.overview && <div className="overview">{it.overview}</div>}
            {it.positionMs > 0 && it.durationMs > 0 && !it.played && (
              <div className="progress">
                <div style={{ width: Math.min(100, (it.positionMs / it.durationMs) * 100) + '%' }} />
              </div>
            )}
            </div>
          </button>
        </li>
      ))}
    </ul>
  )
}

function ShowPage({
  id,
  onBack,
  onPlay,
  onSignedOut,
}: {
  id: string
  onBack: () => void
  onPlay: (id: string) => void
  onSignedOut: () => void
}) {
  const [data, setData] = useState<{ show: Show; seasons: Season[] } | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    api
      .show(id)
      .then(setData)
      .catch((e: ApiError) => (e.status === 401 ? onSignedOut() : setError(e.message)))
  }, [id, onSignedOut])
  return (
    <main>
      <div className="bar">
        <button onClick={onBack}>← Back</button>
        <h1>
          {data?.show.title}
          {data?.show.year ? ` (${data.show.year})` : ''}
        </h1>
      </div>
      {error && <p className="error" role="alert">{error}</p>}
      {data?.show.overview && <p className="dim">{data.show.overview}</p>}
      {data?.seasons.map((se) => (
        <section key={se.number}>
          <h2>{se.title}</h2>
          <ItemList items={se.episodes} onPlay={onPlay} />
        </section>
      ))}
    </main>
  )
}

const mediaErrors: Record<number, string> = {
  1: 'MEDIA_ERR_ABORTED',
  2: 'MEDIA_ERR_NETWORK',
  3: 'MEDIA_ERR_DECODE',
  4: 'MEDIA_ERR_SRC_NOT_SUPPORTED',
}

// What is said when a film plays with sound and no picture. The PC is told
// (as trouble showing the picture), converts the picture for this device
// from then on, and the film is started again converted. These are the
// words for the moment in between, and for when the PC has nothing to offer.
const NO_PICTURE = "This device plays this film's sound but no picture."
const NO_PICTURE_ASKING = NO_PICTURE + ' The PC is being asked to convert the picture into a kind this device can show…'
const NO_PICTURE_STUCK =
  NO_PICTURE + ' The PC has no other way of sending this film that would help. "Open in another player" below may play it.'

const STOPPED =
  'This film stopped while it was being prepared for this browser. Go back and try again; if it keeps happening, export a support bundle.'
const COULD_NOT_PLAY =
  'This browser could not play this file. If the app said it could, that is a mistake worth reporting: export a support bundle from the library page.'

// What the page says after a film was started again another way: the PC's
// sentence about what went wrong and what is different now, or the page's
// own about something the viewer asked for.
interface Notice {
  text: string
  // Set when the change can be taken back: what to forget, the lighter
  // picture the viewer had asked for before, and how the film played before.
  // auto marks a change the app made by itself after trouble. Two of those
  // in a row are taken back together: Undo then puts the film back to how
  // it played before the first.
  undo?: { lessons: string[]; lower: number; method: string; rung: number; auto?: boolean }
}

// earlier is what an automatic change that follows another has to take
// back as well.
function earlier(old: Notice | null): Notice['undo'] | undefined {
  return old && old.undo && old.undo.auto ? old.undo : undefined
}

// playedSeconds is how much of the film has really been played since it was
// handed to the player.
function playedSeconds(v: HTMLVideoElement): number {
  let total = 0
  for (let i = 0; i < v.played.length; i++) total += v.played.end(i) - v.played.start(i)
  return total
}

// How a film is asked for again while it is open.
interface Again {
  // The play it replaces (see PlayWish.from).
  from?: string
  // The viewer had the film paused: it stays paused when it is back.
  paused?: boolean
  then?: (p: PlayInfo) => void
  // Asked only to see whether anything would change. If this says no, the
  // film that is playing stays and the one the PC just opened is closed.
  useIf?: (p: PlayInfo) => boolean
  otherwise?: () => void
}

function Watch({ id, onBack, onSignedOut }: { id: string; onBack: () => void; onSignedOut: () => void }) {
  const [info, setInfo] = useState<PlayInfo | null>(null)
  const [error, setError] = useState('')
  const [problem, setProblem] = useState('')
  const [notice, setNotice] = useState<Notice | null>(null)
  const [waitStage, setWaitStage] = useState<0 | 1 | 2>(0) // see stall.ts: 1 says the film is being got ready, 2 offers ways out
  const [hdrSaid, setHdrSaid] = useState('')
  const [tryAnyway, setTryAnyway] = useState(false)
  const [subKey, setSubKey] = useState('') // which subtitles are on; '' for none
  const videoRef = useRef<HTMLVideoElement | null>(null)
  const noticeRef = useRef<HTMLDivElement | null>(null)
  const infoRef = useRef<PlayInfo | null>(null) // the same as info, for code that runs later
  infoRef.current = info
  const lastSent = useRef(0)
  const reported = useRef<Record<string, boolean>>({})
  const carryOn = useRef<number | null>(null) // where to pick up when the film is started again, in seconds of the film's own time
  const first = useRef(true)
  const chunkPlayer = useRef(false)
  const pictureJudged = useRef(false) // a film that arrives as chunks has been checked for a picture
  const asked = useRef(0) // how many times the film has been asked for
  const left = useRef(false) // the player has been left
  // What the viewer has asked for in this viewing. It goes with every
  // request for the film, so that starting it again for one reason does
  // not undo what was picked for another.
  const pickedAudio = useRef<number | undefined>(undefined)
  const lowerWish = useRef(0) // a lighter picture: the best setting that may be used, 0 for any
  // The watch kept on the player (stall.ts), and what it is told.
  const watch = useRef<StallWatch>(newWatch(performance.now()))
  const pending = useRef(true) // a request for the film is on its way
  const starting = useRef(true) // the film has been handed to the player and has not begun to play
  const needsTap = useRef(false) // the browser waits for the viewer to press Play
  const dead = useRef(false) // the player has given up on what it was handed
  const stayPaused = useRef(false) // the film being handed over is not to start by itself
  const troubleOut = useRef(false) // a report of trouble is on its way
  const stateAsked = useRef(false) // the PC has been asked how this conversion is going
  const noPictureSaid = useRef(false) // what the page says is wrong is "sound but no picture"
  const focusNotice = useRef(false) // the next notice takes the keyboard's place: the button that led to it is gone
  const hdrBusy = useRef(false)
  // The viewer undid a change. From then on nothing is changed by itself
  // for as long as this film is open: they have said how they want it.
  const leftAlone = useRef(false)

  // Each request for the film is numbered and only the newest one's answer
  // is used. When the sound is changed twice in a row the answers can come
  // back in either order, and the first would otherwise use up the place to
  // carry on from. The PC opens the film for every answer, so one that is
  // not used is closed again.
  const load = useCallback(
    (how: Again = {}) => {
      // Once the player has been left nothing more is asked for: an answer
      // that came then would open a film on the PC with nobody to close it.
      if (left.current) return
      const n = ++asked.current
      pending.current = true
      api
        .play(id, { audio: pickedAudio.current, lower: lowerWish.current, from: how.from })
        .then((p) => {
          if (n !== asked.current) {
            if (p.play) api.hlsStop(p.play)
            return
          }
          pending.current = false
          if (how.useIf && !how.useIf(p)) {
            if (p.play) api.hlsStop(p.play)
            carryOn.current = null
            if (how.otherwise) how.otherwise()
            return
          }
          // The place is kept in the film's own time. This player's clock
          // may run a little ahead of it.
          if (carryOn.current !== null) p.positionMs = Math.max(0, Math.floor(carryOn.current * 1000) + (p.clockAheadMs || 0))
          carryOn.current = null
          if (first.current) setSubKey(p.subtitles.find((s) => s.selected)?.key || '')
          first.current = false
          stayPaused.current = !!how.paused
          setInfo(p)
          if (how.then) how.then(p)
        })
        .catch((e: ApiError) => {
          if (n !== asked.current) return
          pending.current = false
          focusNotice.current = false
          if (e.status === 401) onSignedOut()
          else setError(e.message)
        })
    },
    [id, onSignedOut],
  )
  useEffect(() => {
    left.current = false
    first.current = true
    pickedAudio.current = undefined
    lowerWish.current = 0
    leftAlone.current = false
    load()
    // An answer that arrives after the player has been left is not used.
    return () => {
      asked.current++
      left.current = true
    }
  }, [load])

  // keepPlace notes where the film is, so that it carries on from there
  // when it is started again. While it is still starting again after an
  // earlier change its clock reads nothing, and the place to keep is the
  // one it was about to start from.
  const keepPlace = useCallback(() => {
    const v = videoRef.current
    const cur = infoRef.current
    const ahead = ((cur && cur.clockAheadMs) || 0) / 1000
    if (v && v.currentTime > 0) carryOn.current = Math.max(0, v.currentTime - ahead)
    else if (carryOn.current === null && cur) carryOn.current = Math.max(0, cur.positionMs / 1000 - ahead)
  }, [])

  const report = useCallback(
    (kind: string, detail: string) => {
      if (reported.current[kind]) return
      reported.current[kind] = true
      api.clientEvent(kind, id, detail)
    },
    [id],
  )

  // again starts the film afresh where it is, without a word: nothing went
  // wrong that the viewer could do anything about. A film the viewer had
  // paused stays paused.
  const again = useCallback(
    (cur: PlayInfo) => {
      const v = videoRef.current
      keepPlace()
      load({ from: cur.play, paused: !!v && v.paused && !starting.current })
    },
    [load, keepPlace],
  )

  // tell reports trouble in the film that is playing to the PC, which
  // answers what to do about it: start the film again (it has changed how
  // the film is sent, and may have learned something about this device),
  // or leave it. The film is then started again where it is, and the PC's
  // sentence is shown with a way to take the change back.
  //
  // onWait is called with the PC's sentence ('' when it has none) when the
  // film is to be left as it is, or the PC could not be asked. Without it
  // the sentence is simply shown. The result says whether a report went
  // out: one at a time, and none for a film the PC has no name for.
  const tell = useCallback(
    (evidence: Evidence, detail: string, onWait?: (said: string) => void): boolean => {
      const cur = infoRef.current
      if (!cur || !cur.play || troubleOut.current) return false
      troubleOut.current = true
      api
        .trouble(cur.play, evidence, detail)
        .then((ans) => {
          if (infoRef.current !== cur) return // the film has been started again for another reason meanwhile
          if (ans.action !== 'replay') {
            if (onWait) onWait(ans.notice)
            // (What was changed before this can still be taken back.)
            else if (ans.notice) setNotice((old) => ({ text: ans.notice, undo: earlier(old) }))
            return
          }
          keepPlace()
          load({
            from: cur.play,
            then: () => {
              noPictureSaid.current = false
              setProblem('')
              setNotice((old) => {
                const first = earlier(old)
                return {
                  text: ans.notice || 'The film was started again in a way that should play better on this device.',
                  undo: {
                    auto: true,
                    lessons: (first ? first.lessons : []).concat(ans.lessons || []),
                    lower: first ? first.lower : lowerWish.current,
                    method: first ? first.method : cur.plan.method,
                    rung: first ? first.rung : cur.plan.video.rung,
                  },
                }
              })
            },
          })
        })
        .catch((e: ApiError) => {
          if (infoRef.current !== cur) return
          // The PC takes no report about a film it no longer has open (it
          // was restarted, say). That is why the film stands still: it is
          // started again where it is.
          if (e.status === 403) again(cur)
          else if (e.status === 401) onSignedOut()
          // Too many reports in a short time, or the PC is out of reach:
          // the film is left as it is.
          else if (onWait) onWait('')
        })
        .then(() => {
          troubleOut.current = false
        })
      return true
    },
    [load, keepPlace, again, onSignedOut],
  )

  // askPC asks how the film is doing on the PC. If the PC no longer has it
  // open (it was restarted, say, while the phone was locked), the film is
  // started again. If the PC says it converts slower than the film plays,
  // that is reported as the trouble it is about to become, without waiting
  // for the film to stall. otherwise is called when the film is still open.
  const askPC = useCallback(
    (otherwise?: () => void) => {
      const cur = infoRef.current
      if (!cur || !cur.play) return otherwise && otherwise()
      api
        .playState(cur.play)
        .then((st) => {
          if (infoRef.current !== cur) return
          if (!st.alive) return again(cur)
          const v = videoRef.current
          if (st.advice === 'lower' && !leftAlone.current && v && !v.paused)
            tell('starved', `the PC said it converts ${st.pace} seconds of film a second`)
          if (otherwise) otherwise()
        })
        .catch((e: ApiError) => {
          if (infoRef.current !== cur) return
          if (e.status === 401) onSignedOut()
          else if (otherwise) otherwise()
        })
    },
    [again, tell, onSignedOut],
  )

  const save = useCallback(
    (force: boolean) => {
      const v = videoRef.current
      if (!v || !isFinite(v.duration) || v.duration <= 0) return
      const now = Date.now()
      if (!force && now - lastSent.current < 10000) return
      lastSent.current = now
      api.progress(id, Math.floor(v.currentTime * 1000), Math.floor(v.duration * 1000)).catch(() => undefined)
    },
    [id],
  )

  useEffect(() => {
    const onHide = () => save(true)
    window.addEventListener('pagehide', onHide)
    document.addEventListener('visibilitychange', onHide)
    return () => {
      onHide()
      window.removeEventListener('pagehide', onHide)
      document.removeEventListener('visibilitychange', onHide)
    }
  }, [save])

  // When the page is looked at again (the phone was unlocked, the tab was
  // brought back), the film may have been closed on the PC meanwhile.
  useEffect(() => {
    const shown = () => {
      if (document.visibilityState !== 'visible') return
      mark(watch.current, performance.now())
      askPC()
    }
    const onShow = (e: PageTransitionEvent) => {
      if (e.persisted) shown()
    }
    document.addEventListener('visibilitychange', shown)
    window.addEventListener('pageshow', onShow)
    return () => {
      document.removeEventListener('visibilitychange', shown)
      window.removeEventListener('pageshow', onShow)
    }
  }, [askPC])

  // Twice a second the player is looked at (see stall.ts): how long the
  // viewer has been waiting for a film that should be moving, and whether
  // it is in trouble the PC should hear about.
  useEffect(() => {
    const t = window.setInterval(() => {
      const v = videoRef.current
      const now = performance.now()
      const hidden = document.visibilityState === 'hidden'
      const seen =
        v && !dead.current
          ? reading(v, now, starting.current && !needsTap.current, hidden)
          : { now, playing: false, starting: !v && pending.current, time: 0, ready: 0, hidden }
      const found = look(watch.current, seen)
      setWaitStage(found.stage)
      if (found.trouble && !leftAlone.current) tell(found.trouble.evidence, found.trouble.detail)
    }, RULES.everyMs)
    return () => window.clearInterval(t)
  }, [tell])

  const showPlayer = !!info && (info.plan.playable !== 'no' || tryAnyway)

  // Subtitles: the chosen track is switched on and every other off. A
  // track that is off is not even downloaded. None is switched on before
  // the film is playing, because a browser will not start a film while
  // subtitles that were already on when it began loading are still on
  // their way, and subtitles stored inside a film take as long to arrive
  // as the PC takes to read the film through. So each time the film is
  // handed to the player they are all switched off, and the chosen one is
  // switched on once the film plays.
  const started = useRef(false) // the film has played since it was last handed to the player
  const subWanted = useRef('') // the same as subKey, for code that runs between two drawings of the page
  const applySubs = useCallback(() => {
    const tracks = videoRef.current ? videoRef.current.querySelectorAll('track') : []
    for (let i = 0; i < tracks.length; i++) {
      const want = started.current && tracks[i].getAttribute('data-key') === subWanted.current ? 'showing' : 'disabled'
      if (tracks[i].track.mode !== want) tracks[i].track.mode = want
    }
  }, [])
  const subsOff = useCallback(() => {
    started.current = false
    applySubs()
  }, [applySubs])
  // Only the first time the film plays after being handed over: later (the
  // film plays again after every jump) whatever is showing is left alone,
  // which may be subtitles the viewer switched on in the browser's own menu.
  const onPlaying = () => {
    if (starting.current) {
      // The start is over. A moment's wait just after it is still normal.
      starting.current = false
      mark(watch.current, performance.now())
    }
    if (started.current) return
    started.current = true
    applySubs()
  }

  // Hand the address to the player. The original file, and a list of
  // chunks where the browser plays those by itself (Safari, every iPhone),
  // go straight in. Elsewhere the chunks are played by hls.js, which is
  // only downloaded when it is needed.
  useEffect(() => {
    const v = videoRef.current
    if (!info || !showPlayer || !v) return
    const token = info.play || info.hls
    const stop = () => token && api.hlsStop(token)
    // Leaving the page for good stops the conversion at once. But a browser
    // may only put the page aside, to bring it back exactly as it was when
    // the viewer returns to it (Back after a visit to another page; Safari
    // on an iPhone does this readily). The film must then still be open on
    // the PC, or the player that comes back holds addresses that no longer
    // work. If the page never comes back, the PC stops converting by itself
    // after a little over a minute without requests.
    const onHide = (e: PageTransitionEvent) => {
      if (e.persisted) return
      // The page is going for good. The film is closed on the PC, and must
      // not be asked for again by a player that finds its film closed.
      left.current = true
      stop()
    }
    window.addEventListener('pagehide', onHide)
    chunkPlayer.current = false
    pictureJudged.current = false
    stateAsked.current = false
    dead.current = false
    needsTap.current = false
    starting.current = true
    // A new watch for a new start. A wait that is already under way (for
    // the PC's answer, or for the film this one replaces) goes on counting.
    watch.current = newWatch(performance.now(), watch.current.waitSince)
    subsOff()
    if (!info.hls || builtInChunks()) {
      v.src = info.url
      return () => {
        window.removeEventListener('pagehide', onHide)
        stop()
        v.removeAttribute('src')
        v.load()
      }
    }
    chunkPlayer.current = true
    let gone = false
    let player: { destroy: () => void } | null = null
    import('hls.js/light')
      .then(({ default: Hls }) => {
        if (gone) return
        const hls = new Hls({
          // Resume, unless that would land in the last few seconds.
          startPosition: info.positionMs > 0 && info.positionMs < info.durationMs - 5000 ? info.positionMs / 1000 : -1,
          maxBufferLength: 30,
          maxMaxBufferLength: 60,
          // A chunk may take a while to begin arriving: after a jump the PC
          // has to convert the film up to it first. Only after half a
          // minute is it taken for lost and asked for again.
          fragLoadPolicy: { default: { ...Hls.DefaultConfig.fragLoadPolicy.default, maxTimeToFirstByteMs: 30000 } },
        })
        player = hls
        let recovered = false
        let refused = false
        hls.on(Hls.Events.ERROR, (_e, data) => {
          // Parts of a film are refused once the PC no longer has the film
          // open (it was restarted, or this sign-in has opened many films
          // since). The player may go on asking for a while before it
          // gives up; the PC is asked at the first refusal, and if the
          // film is indeed gone it is started again where it is.
          if (data.response && data.response.code === 403 && !refused) {
            refused = true
            if (data.fatal) dead.current = true
            askPC(data.fatal ? () => setProblem(STOPPED) : undefined)
            return
          }
          // The player running dry (hls.js calls it bufferStalledError) is
          // seen by the watch itself, in every browser. A chunk that did
          // not begin to arrive in half a minute is trouble of the same
          // kind even if the player still holds film to show meanwhile.
          if (data.details === Hls.ErrorDetails.FRAG_LOAD_TIMEOUT && !leftAlone.current) {
            const late = lateChunk(watch.current, performance.now(), !v.paused && !v.ended)
            if (late) tell(late.evidence, late.detail)
          }
          if (!data.fatal) return
          if (data.type === Hls.ErrorTypes.MEDIA_ERROR && !recovered) {
            recovered = true
            subsOff() // recovering hands the film to the player afresh
            hls.recoverMediaError()
            return
          }
          dead.current = true
          const stopped = () => {
            setProblem(STOPPED)
            report('convert-error', `${info.container} ${info.plan.method} ${data.type} ${data.details}`)
          }
          if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
            // The player has given up on the picture twice. The PC may
            // have an easier one to send.
            if (leftAlone.current || !tell('decode', `the player gave up on the film: ${data.details}`, (said) => (said ? setProblem(said) : stopped())))
              stopped()
            return
          }
          stopped()
        })
        hls.loadSource(info.url)
        hls.attachMedia(v)
      })
      .catch(() => setProblem('The part of the app that plays repackaged video could not be loaded. Reload the page and try again.'))
    return () => {
      gone = true
      window.removeEventListener('pagehide', onHide)
      if (player) player.destroy()
      stop()
    }
  }, [info, showPlayer, report, subsOff, tell, askPC])

  // A choice of subtitles takes effect at once if the film is playing. If
  // it is not yet, the choice is kept and takes effect when it is.
  useEffect(() => {
    subWanted.current = subKey
    applySubs()
  }, [info, showPlayer, subKey, applySubs])

  // A notice that follows the press of a button takes the keyboard's place
  // on the page: the button is gone, or will be in a moment.
  useEffect(() => {
    if (!notice || !focusNotice.current) return
    focusNotice.current = false
    if (noticeRef.current) noticeRef.current.focus()
  }, [notice])

  const remember = (audioIndex: number, sub: string) => {
    if (!info) return
    const a = info.audio[audioIndex]
    const s = info.subtitles.find((x) => x.key === sub)
    api.choice(id, { audioLang: a?.language || '', subLang: s?.language || '', subsOn: !!s, audioIndex: a ? audioIndex : -1, subKey: s ? s.key : '' })
  }
  const pickAudio = (index: number) => {
    keepPlace()
    remember(index, subKey)
    pickedAudio.current = index
    load({ from: info ? info.play : undefined })
  }
  const pickSub = (key: string) => {
    setSubKey(key)
    remember(info ? info.plan.audioTrack : -1, key)
  }

  // The same film as before? Starting it again would then be for nothing.
  const sameWay = (p: PlayInfo, q: PlayInfo) => p.plan.method === q.plan.method && p.plan.video.rung === q.plan.video.rung

  // lighter asks for the film with a lighter picture: quicker for the PC to
  // make and for the connection to carry.
  const lighter = () => {
    const cur = infoRef.current
    if (!cur || !cur.lighter) return
    const before = lowerWish.current
    lowerWish.current = cur.lighter
    focusNotice.current = true
    keepPlace()
    load({
      from: cur.play,
      useIf: (p) => !sameWay(p, cur),
      then: () =>
        setNotice({
          text: 'The film now plays with a lighter picture, which is quicker for the PC to make and to send.',
          undo: { lessons: [], lower: before, method: cur.plan.method, rung: cur.plan.video.rung },
        }),
      otherwise: () => {
        lowerWish.current = before
        setNotice({ text: 'This is already the lightest picture this PC can make of this film.' })
      },
    })
  }

  // undo takes back the change the notice is about: what the app learned
  // from it is forgotten, and the film is started again as a new viewing
  // would start it.
  const undo = () => {
    const u = notice && notice.undo
    if (!u) return
    leftAlone.current = true
    lowerWish.current = u.lower
    focusNotice.current = true
    Promise.all(u.lessons.map((l) => api.forgetLesson(l).catch(() => undefined))).then(() => {
      keepPlace()
      load({
        then: (p) =>
          setNotice({
            text:
              p.plan.method === u.method && p.plan.video.rung === u.rung
                ? 'Undone: the film plays the way it did before' +
                  (u.lessons.length > 0 ? ', and what the app had just learned is forgotten' : '') +
                  '. Nothing more is changed by itself while this film is open.'
                : 'The change could not be taken back completely: the PC still sends this film another way than before. If that comes from what the PC has learned about its own speed, the owner can have it forgotten under Settings.',
          }),
      })
    })
  }

  // The viewer says HDR colours look wrong on this device. The PC notes it
  // and from then on converts HDR films to normal colour for this device,
  // where it can. Where it cannot, the film that is playing is left alone.
  const coloursWrong = () => {
    const cur = infoRef.current
    if (!cur || hdrBusy.current) return
    hdrBusy.current = true
    setHdrSaid('One moment…')
    api
      .hdrWrong(true)
      .then((r) => {
        focusNotice.current = true
        keepPlace()
        load({
          from: cur.play,
          useIf: (p) => !sameWay(p, cur),
          then: () => {
            setHdrSaid('')
            setNotice({
              text: 'Noted. HDR films are now converted to normal colour for this device.',
              undo: { lessons: r.lesson ? [r.lesson] : [], lower: lowerWish.current, method: cur.plan.method, rung: cur.plan.video.rung },
            })
          },
          otherwise: () => {
            focusNotice.current = false
            setHdrSaid(
              'Noted. It is listed in Settings under "Learned about this device". Nothing changes for this film, though: this PC cannot convert its colours for this device. "Open in another player" below may show them right.',
            )
          },
        })
      })
      .catch((e: ApiError) => (e.status === 401 ? onSignedOut() : setHdrSaid(e.message)))
      .then(() => {
        hdrBusy.current = false
      })
  }

  const noPicture = () => {
    report('no-picture', `${info?.container} videoWidth=0`)
    noPictureSaid.current = true
    const stuck = (said: string) => {
      if (noPictureSaid.current) setProblem(said ? NO_PICTURE + ' ' + said : NO_PICTURE_STUCK)
    }
    if (!leftAlone.current && tell('decode', 'sound but no picture: the picture has no size', stuck)) setProblem(NO_PICTURE_ASKING)
    else stuck('')
  }

  const onLoaded = () => {
    const v = videoRef.current
    if (!v || !info) return
    // hls.js was told where to start; everything else is told here.
    if (!chunkPlayer.current && info.positionMs > 0 && info.positionMs / 1000 < v.duration - 5) v.currentTime = info.positionMs / 1000
    applySubs() // a track the browser switched on by itself goes off again, like the rest
    if (stayPaused.current) {
      // The viewer had the film paused when it was started again.
      stayPaused.current = false
      needsTap.current = true
      v.pause()
    } else if (v.paused) {
      // The film starts by itself where the browser lets it. It is asked
      // here as well, because the answer tells two waits apart: a browser
      // that will not start a film until the viewer presses Play says so
      // at once, and one that is still fetching the film does not.
      const going = v.play()
      if (going)
        going.catch((e: unknown) => {
          if (e && (e as { name?: string }).name === 'NotAllowedError') needsTap.current = true
        })
    }
    // An ordinary file knows the size of its picture by now, so no size
    // means no picture. A film that arrives as chunks may not know it yet:
    // Safari's own chunk player learns it a moment later. That case is
    // judged in onTime, once the film has really been playing.
    if (!info.hls && v.videoWidth === 0) noPicture()
  }

  // A browser that stops fetching a film it has not begun to play is
  // waiting for the viewer to press Play (an iPhone saving data does this).
  const onSuspend = () => {
    const v = videoRef.current
    if (v && v.paused && starting.current) needsTap.current = true
  }

  // The size of the picture has become known, so there is a picture after
  // all.
  const onResize = () => {
    const v = videoRef.current
    if (v && v.videoWidth > 0 && noPictureSaid.current) {
      noPictureSaid.current = false
      setProblem('')
    }
  }

  const onTime = () => {
    save(false)
    const v = videoRef.current as (HTMLVideoElement & { webkitAudioDecodedByteCount?: number }) | null
    // Chrome can tell us that no sound has been decoded: the usual sign of
    // Dolby or DTS sound, which Chrome cannot play. Only when the server
    // could not tell us: a file it has inspected is handled before playing.
    if (v && info?.plan.hasAudio === null && v.currentTime > 3 && v.webkitAudioDecodedByteCount === 0 && !v.muted) {
      setProblem(
        'If you hear no sound: this file has not been inspected yet, so the app could not tell that its sound (probably Dolby or DTS) needs converting for this browser. Go back and try again in a minute.',
      )
      report('no-sound', `${info?.container} webkitAudioDecodedByteCount=0`)
    }
    // A film that arrives as chunks is judged here, and like an ordinary
    // file only once: after three seconds of playing every browser knows
    // the size of the picture, if there is one.
    if (v && info?.hls && !pictureJudged.current && playedSeconds(v) > 3) {
      pictureJudged.current = true
      if (v.videoWidth === 0) noPicture()
    }
    // Twelve seconds into a film whose picture is being converted, the PC
    // knows how fast it is going. If that is slower than the film plays,
    // it is better to step down now than to wait for the film to stall.
    if (v && info?.plan.method === 'convert' && !stateAsked.current && playedSeconds(v) > 12) {
      stateAsked.current = true
      askPC()
    }
  }

  const onError = () => {
    const v = videoRef.current
    if (chunkPlayer.current) return // hls.js reports its own errors, with more detail
    const code = v?.error?.code || 0
    dead.current = true
    const could = () => {
      setProblem(COULD_NOT_PLAY)
      report('play-error', `${info?.container} ${info?.plan.method} ${mediaErrors[code] || 'code ' + code} ${v?.error?.message || ''}`.trim())
    }
    // The browser began on the film and gave up on what is in it: the PC
    // may have an easier picture to send.
    if (code === 3 && !leftAlone.current && tell('decode', 'the browser gave up on the film while playing it', (said) => (said ? setProblem(said) : could()))) return
    // A link to a file runs out after some hours, and stops working when
    // the PC is restarted: the film is then simply started again.
    if (code === 2 || code === 4) askPC(could)
    else could()
  }

  // "Open in another player" is offered under the player and where a film
  // is said not to play here. Another player starts the film where it is
  // on this page, or where it was last stopped if it is not playing here;
  // and once it has the film, the film stops here.
  const elsewhere = (
    <OpenElsewhere
      key={id}
      id={id}
      position={() => {
        const v = videoRef.current
        return v && v.currentTime > 0 ? v.currentTime : (info?.positionMs || 0) / 1000
      }}
      onLeave={() => videoRef.current?.pause()}
      onSignedOut={onSignedOut}
    />
  )
  const unplayable = !!info && info.plan.playable === 'no' && !tryAnyway
  // Where the film is, or is on its way: the player, or the wait for the
  // PC's first answer.
  const waitingRoom = showPlayer || (!info && !error)

  return (
    <main>
      <div className="bar">
        <button onClick={onBack}>← Back</button>
        <h1 style={{ fontSize: '1.2rem' }}>{info?.title || ''}</h1>
      </div>
      {error && <p className="error" role="alert">{error}</p>}
      {problem && <p className="note" role="alert" data-testid="play-problem">{problem}</p>}
      <div className={notice ? 'notice' : undefined} role="status" tabIndex={-1} ref={noticeRef} data-testid="step-notice">
        {notice && (
          <>
            <p data-testid="step-said">{notice.text}</p>
            {notice.undo && (
              <p>
                <button type="button" data-testid="step-undo" onClick={undo}>
                  Undo
                </button>{' '}
                <span className="dim">
                  Undo plays the film the way it was
                  {notice.undo.lessons.length > 0 ? ', and the app forgets what it has just learned' : ''}.
                </span>
              </p>
            )}
          </>
        )}
      </div>
      {info && unplayable && (
        <div className="card" role="alert" data-testid="play-problem">
          <p>{info.explain}</p>
          <ul>
            {info.plan.reasons.map((r) => (
              <li key={r}>{r}</li>
            ))}
          </ul>
          {!info.plan.pcCannot && <p className="dim">Another browser or device may be able to play it today.</p>}
          <button onClick={() => setTryAnyway(true)}>Try anyway</button>
          {elsewhere}
        </div>
      )}
      {info && showPlayer && (
        <>
          <video
            ref={videoRef}
            controls
            autoPlay
            playsInline
            preload="metadata"
            data-method={info.plan.method}
            data-rung={info.plan.method === 'convert' ? info.plan.video.rung : undefined}
            data-subtitles={info.plan.subtitles !== 'none' ? info.plan.subtitles : subKey ? 'track' : 'none'}
            onLoadedMetadata={onLoaded}
            onPlaying={onPlaying}
            onSeeking={() => mark(watch.current, performance.now())}
            onSuspend={onSuspend}
            onResize={onResize}
            onTimeUpdate={onTime}
            onPause={() => save(true)}
            onEnded={() => save(true)}
            onError={onError}
          >
            {info.subtitles.map((s) => (
              <track key={s.key} data-key={s.key} kind="subtitles" src={s.url} srcLang={s.language || 'und'} label={s.label} />
            ))}
          </video>
          <div className="bar picks">
            {info.audio.length > 1 && (
              <label>
                Sound{' '}
                <select data-testid="pick-sound" value={info.plan.audioTrack} onChange={(e) => pickAudio(Number(e.target.value))}>
                  {info.audio.map((a) => (
                    <option key={a.index} value={a.index}>
                      {a.label}
                    </option>
                  ))}
                </select>
              </label>
            )}
            {info.subtitles.length > 0 && (
              <label>
                Subtitles{' '}
                <select data-testid="pick-subtitles" value={subKey} onChange={(e) => pickSub(e.target.value)}>
                  <option value="">Off</option>
                  {info.subtitles.map((s) => (
                    <option key={s.key} value={s.key}>
                      {s.label}
                    </option>
                  ))}
                </select>
              </label>
            )}
          </div>
        </>
      )}
      {waitingRoom && (
        // What the page says about a film that should be moving and is
        // not: these words after three seconds, ways out after twelve.
        <div role="status" data-testid="wait-words">
          {waitStage >= 1 && <p className="dim">Still getting the film ready…</p>}
        </div>
      )}
      {info && showPlayer && (
        <>
          {info.pictureSubtitles > 0 && (
            <p className="dim">
              This file also has {info.pictureSubtitles} subtitle {info.pictureSubtitles === 1 ? 'track' : 'tracks'} stored as pictures
              (as on discs). Showing those arrives in a later version (M4).
            </p>
          )}
          <details className="how" data-testid="how-playing">
            <summary>{info.plan.method === 'direct' ? 'How is this playing?' : 'Why is this converting?'}</summary>
            <p data-testid="how-text">{info.explain}</p>
            {info.plan.reasons.length > 0 && (
              <>
                <p className="dim">Why the original file is not simply sent to this browser:</p>
                <ul>
                  {info.plan.reasons.map((r) => (
                    <li key={r}>{r}</li>
                  ))}
                </ul>
              </>
            )}
            {info.plan.lost.length > 0 && (
              <>
                <p className="dim">What is different from the original:</p>
                <ul data-testid="how-lost">
                  {info.plan.lost.map((r) => (
                    <li key={r}>{r}</li>
                  ))}
                </ul>
              </>
            )}
            {info.hdr && (
              <div data-testid="hdr">
                <p className="dim">
                  This film is in HDR (extra bright and rich colour) and is sent as it is. If its colours look washed out, dull or odd on this
                  screen, say so: the PC then converts HDR films to normal colour for this device, where it can.
                </p>
                <button type="button" data-testid="hdr-wrong" onClick={coloursWrong}>
                  Colours look wrong on this device
                </button>
                <div role="status" data-testid="hdr-said">
                  {hdrSaid && <p>{hdrSaid}</p>}
                </div>
              </div>
            )}
          </details>
        </>
      )}
      {waitingRoom && (
        // After twelve seconds of waiting this becomes a card with ways
        // out. "Open in another player" is in it, and stays where it is
        // when the card comes and goes, so the keyboard's place on it is
        // never lost.
        <div className={waitStage === 2 ? 'card waiting' : undefined} data-testid={waitStage === 2 ? 'wait-card' : undefined}>
          {waitStage === 2 && (
            <>
              <p>This is taking a long time. You can go on waiting, or:</p>
              <div className="bar">
                {info && showPlayer && info.lighter > 0 && (
                  <button type="button" data-testid="wait-lighter" onClick={lighter}>
                    Play with a lighter picture
                  </button>
                )}
                <button type="button" data-testid="wait-back" onClick={onBack}>
                  Go back
                </button>
              </div>
            </>
          )}
          {(showPlayer || waitStage === 2) && elsewhere}
        </div>
      )}
    </main>
  )
}

const LANGUAGES: [string, string][] = [
  ['en', 'English'], ['no', 'Norwegian'], ['sv', 'Swedish'], ['da', 'Danish'], ['fi', 'Finnish'], ['de', 'German'],
  ['fr', 'French'], ['es', 'Spanish'], ['it', 'Italian'], ['pt', 'Portuguese'], ['nl', 'Dutch'], ['pl', 'Polish'],
  ['ja', 'Japanese'], ['ko', 'Korean'], ['zh', 'Chinese'],
]

// PlaybackCard holds each viewer's own sound and subtitle preferences, and
// says what this device and this PC can do: the facts behind every
// "why is this converting?".
function PlaybackCard() {
  const [prefs, setPrefs] = useState<Prefs | null>(null)
  const [diag, setDiag] = useState<Diagnostics | null>(null)
  const [msg, setMsg] = useState('')
  useEffect(() => {
    api.prefs().then(setPrefs).catch(() => undefined)
    api.diagnostics().then(setDiag).catch(() => undefined)
  }, [])
  if (!prefs) return null
  const save = (p: Prefs) => {
    setPrefs(p)
    api
      .setPrefs(p)
      .then(() => setMsg('Saved.'))
      .catch((e: ApiError) => setMsg(e.message))
  }
  const list = (m: Record<string, boolean> | undefined) =>
    Object.keys(m || {})
      .filter((k) => m![k])
      .join(', ') || 'none'
  return (
    <div className="card" data-testid="playback-card">
      <h2 style={{ marginTop: 0 }}>Sound and subtitles</h2>
      <p className="dim">
        Your own choices, used wherever you sign in. What you pick while watching is remembered for that show or film.
      </p>
      <p>
        <label>
          Sound in{' '}
          <select data-testid="pref-sound" value={prefs.audioLang} onChange={(e) => save({ ...prefs, audioLang: e.target.value })}>
            <option value="">whatever the file marks as its main sound</option>
            {LANGUAGES.map(([code, name]) => (
              <option key={code} value={code}>
                {name}
              </option>
            ))}
          </select>
        </label>
      </p>
      <p>
        <label>
          Subtitles in{' '}
          <select data-testid="pref-subtitles" value={prefs.subLang} onChange={(e) => save({ ...prefs, subLang: e.target.value })}>
            <option value="">the same language as the sound</option>
            {LANGUAGES.map(([code, name]) => (
              <option key={code} value={code}>
                {name}
              </option>
            ))}
          </select>
        </label>
      </p>
      <p>
        <label>
          Show subtitles{' '}
          <select data-testid="pref-when" value={prefs.subs} onChange={(e) => save({ ...prefs, subs: e.target.value as Prefs['subs'] })}>
            <option value="auto">when the sound is in another language</option>
            <option value="always">always</option>
            <option value="off">never</option>
          </select>
        </label>
      </p>
      {msg && <p className="dim" role="status">{msg}</p>}
      {diag && (
        <details className="how" data-testid="diagnostics">
          <summary>What this device can play</summary>
          <p>
            {diag.converter
              ? 'This PC can repackage files, and convert their sound, for devices that cannot play them as they are.'
              : 'This PC has no converter (ffmpeg was not found beside the program), so only files a device can play as they are will play.'}
            {diag.running > 0 && ` Being converted right now: ${diag.running}.`}
          </p>
          {diag.device ? (
            <ul>
              <li>File types it opens: {list(diag.device.containers)}</li>
              <li>Picture formats: {list(diag.device.video)}</li>
              <li>Sound formats: {list(diag.device.audio)}</li>
              <li>
                Repackaged video:{' '}
                {!diag.device.hls ? 'no' : builtInChunks() ? 'yes, built into this browser' : "yes, through the app's own player"}
              </li>
            </ul>
          ) : (
            <p className="dim">This device has not reported what it can play yet. Reload the page.</p>
          )}
          <p className="dim">The support bundle includes this list for each device that has signed in.</p>
        </details>
      )}
    </div>
  )
}
