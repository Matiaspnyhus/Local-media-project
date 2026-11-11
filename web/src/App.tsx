import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { api, ApiError, type Folders, type Home, type Item, type PlayInfo, type Season, type Show, type State } from './api'
import { measureCaps } from './caps'

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
    api
      .capabilities(measureCaps())
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

const NEEDS = 'Needs converting, which arrives in a later version'

function itemLabel(it: Item, withShow: boolean): string {
  if (it.kind !== 'episode') return it.title + (it.year ? ` (${it.year})` : '')
  const ep = it.episodeEnd ? `Episodes ${it.episode}–${it.episodeEnd}` : `Episode ${it.episode}`
  return withShow ? `${it.showTitle} · Season ${it.season} · ${ep}` : ep
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
  const pending = home?.inspection.available ? home.inspection.pending : 0
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
        <button onClick={signOut}>Sign out</button>
      </div>

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
              {tab === 'films' ? <ItemList items={movies} onPlay={onPlay} withShow /> : <ShowList shows={shows} onShow={onShow} />}
              {tab === 'films' && movies.length === 0 && <p className="dim">No films found.</p>}
              {tab === 'shows' && shows.length === 0 && <p className="dim">No shows found.</p>}
            </section>
          )}
        </>
      )}
      <p className="dim" style={{ marginTop: '2rem' }}>
        Local Media {state.version}. This early version plays only files your browser can play by itself. The others
        are marked; converting them arrives in a later version.
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
          <button className="row" onClick={() => onShow(sh.id)}>
            <div className="title">
              {sh.title}
              {sh.year ? ` (${sh.year})` : ''}
            </div>
            <div className="meta">
              {sh.episodes} {sh.episodes === 1 ? 'episode' : 'episodes'}
              {sh.watched > 0 ? ` · ${sh.watched} watched` : ''}
            </div>
          </button>
        </li>
      ))}
    </ul>
  )
}

function ItemList({ items, onPlay, withShow }: { items: Item[]; onPlay: (id: string) => void; withShow?: boolean }) {
  return (
    <ul className="list">
      {items.map((it) => (
        <li key={it.id}>
          <button className="row" onClick={() => onPlay(it.id)}>
            <div className="title">{itemLabel(it, !!withShow)}</div>
            <div className="meta">
              {it.durationMs > 0 ? formatTime(it.durationMs) + ' · ' : ''}
              {it.container.toUpperCase()}
              {it.played ? ' · Watched' : it.positionMs > 0 ? ` · Stopped at ${formatTime(it.positionMs)}` : ''}
            </div>
            {it.playable === 'no' && <div className="needs">{NEEDS}</div>}
            {it.positionMs > 0 && it.durationMs > 0 && !it.played && (
              <div className="progress">
                <div style={{ width: Math.min(100, (it.positionMs / it.durationMs) * 100) + '%' }} />
              </div>
            )}
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

function Watch({ id, onBack, onSignedOut }: { id: string; onBack: () => void; onSignedOut: () => void }) {
  const [info, setInfo] = useState<PlayInfo | null>(null)
  const [error, setError] = useState('')
  const [problem, setProblem] = useState('')
  const [tryAnyway, setTryAnyway] = useState(false)
  const videoRef = useRef<HTMLVideoElement | null>(null)
  const lastSent = useRef(0)
  const reported = useRef<Record<string, boolean>>({})

  useEffect(() => {
    api
      .play(id)
      .then(setInfo)
      .catch((e: ApiError) => (e.status === 401 ? onSignedOut() : setError(e.message)))
  }, [id, onSignedOut])

  const report = useCallback(
    (kind: string, detail: string) => {
      if (reported.current[kind]) return
      reported.current[kind] = true
      api.clientEvent(kind, id, detail)
    },
    [id],
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

  const onLoaded = () => {
    const v = videoRef.current
    if (!v || !info) return
    if (info.positionMs > 0 && info.positionMs / 1000 < v.duration - 5) v.currentTime = info.positionMs / 1000
    if (v.videoWidth === 0) {
      setProblem(
        "This browser can play this file's sound but not its picture. Converting such files arrives in a later version (M4).",
      )
      report('no-picture', `${info.container} videoWidth=0`)
    }
  }

  const onTime = () => {
    save(false)
    const v = videoRef.current as (HTMLVideoElement & { webkitAudioDecodedByteCount?: number }) | null
    // Chrome can tell us that no sound has been decoded: the usual sign of
    // Dolby or DTS sound, which Chrome cannot play.
    // Only when the server could not tell us: if it inspected the file it
    // already knows whether there is sound, and said so before playing.
    if (v && info?.plan.hasAudio === null && v.currentTime > 3 && v.webkitAudioDecodedByteCount === 0 && !v.muted) {
      setProblem(
        "If you hear no sound: this file's sound format (probably Dolby or DTS) is one this browser cannot play. Converting it arrives in a later version (M3).",
      )
      report('no-sound', `${info?.container} webkitAudioDecodedByteCount=0`)
    }
  }

  const onError = () => {
    const v = videoRef.current
    const code = v?.error?.code || 0
    setProblem(
      'This browser cannot play this file by itself. That is expected for many files in this early version; converting them arrives in a later version (M3).',
    )
    report('play-error', `${info?.container} ${mediaErrors[code] || 'code ' + code} ${v?.error?.message || ''}`.trim())
  }

  return (
    <main>
      <div className="bar">
        <button onClick={onBack}>← Back</button>
        <h1 style={{ fontSize: '1.2rem' }}>{info?.title || ''}</h1>
      </div>
      {error && <p className="error" role="alert">{error}</p>}
      {problem && <p className="note" role="alert" data-testid="play-problem">{problem}</p>}
      {info && info.plan.direct === 'no' && !tryAnyway && (
        <div className="card" role="alert" data-testid="play-problem">
          <p>This browser cannot play this file as it is.</p>
          <ul>
            {info.plan.reasons.map((r) => (
              <li key={r}>{r}</li>
            ))}
          </ul>
          <p className="dim">
            Converting files like this one while you watch arrives in a later version (M3). Another browser or device
            may be able to play it today.
          </p>
          <button onClick={() => setTryAnyway(true)}>Try anyway</button>
        </div>
      )}
      {info && (info.plan.direct !== 'no' || tryAnyway) && (
        <video
          ref={videoRef}
          src={info.url}
          controls
          autoPlay
          playsInline
          preload="metadata"
          onLoadedMetadata={onLoaded}
          onTimeUpdate={onTime}
          onPause={() => save(true)}
          onEnded={() => save(true)}
          onError={onError}
        />
      )}
    </main>
  )
}
