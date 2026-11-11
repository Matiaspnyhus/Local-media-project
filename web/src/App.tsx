import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { api, ApiError, type Folders, type Item, type PlayInfo, type State } from './api'

type Screen = { name: 'library' } | { name: 'watch'; id: string }

function screenFromLocation(): Screen {
  const m = window.location.pathname.match(/^\/watch\/([0-9a-f]{32})$/)
  return m ? { name: 'watch', id: m[1] } : { name: 'library' }
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
    window.history.pushState(null, '', s.name === 'watch' ? `/watch/${s.id}` : '/')
    setScreen(s)
  }

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
  if (screen.name === 'watch')
    return <Watch id={screen.id} onBack={() => go({ name: 'library' })} onSignedOut={refresh} />
  return <Library state={state} onPlay={(id) => go({ name: 'watch', id })} onSignedOut={refresh} />
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

function Library({ state, onPlay, onSignedOut }: { state: State; onPlay: (id: string) => void; onSignedOut: () => void }) {
  const [items, setItems] = useState<Item[] | null>(null)
  const [error, setError] = useState('')
  const [note, setNote] = useState('')
  const [urls, setUrls] = useState<string[]>([])
  const [showConnect, setShowConnect] = useState(false)
  const isOwner = state.user?.role === 'owner'

  const load = useCallback(() => {
    api
      .items()
      .then((r) => setItems(r.items))
      .catch((e: ApiError) => (e.status === 401 ? onSignedOut() : setError(e.message)))
  }, [onSignedOut])
  useEffect(load, [load])
  useEffect(() => {
    api.connect().then((r) => setUrls(r.urls)).catch(() => undefined)
  }, [])

  const rescan = () => {
    setNote('Looking for new files…')
    api
      .scan()
      .then((r) => {
        setNote(`Found ${r.scan.total} video files (${r.scan.added} new).`)
        load()
      })
      .catch((e: ApiError) => setNote(e.message))
  }
  const signOut = () => api.logout().then(onSignedOut, onSignedOut)

  const continuing = (items || []).filter((i) => i.positionMs > 0 && !i.played)

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
      {items === null && !error && <p className="dim">Loading…</p>}
      {items && items.length === 0 && (
        <div className="card">
          <p>No video files were found in your folder yet.</p>
          <p className="dim">Copy a video into the folder and press "Look for new files".</p>
        </div>
      )}

      {continuing.length > 0 && (
        <>
          <h2>Continue watching</h2>
          <ItemList items={continuing} onPlay={onPlay} />
        </>
      )}
      {items && items.length > 0 && (
        <>
          <h2>All videos ({items.length})</h2>
          <ItemList items={items} onPlay={onPlay} />
        </>
      )}
      <p className="dim" style={{ marginTop: '2rem' }}>
        Local Media {state.version}. This early version plays only files your browser can play by itself. Others will
        say so when you try them; that is expected until a later version.
      </p>
    </main>
  )
}

function ItemList({ items, onPlay }: { items: Item[]; onPlay: (id: string) => void }) {
  return (
    <ul className="list">
      {items.map((it) => (
        <li key={it.id}>
          <button className="row" onClick={() => onPlay(it.id)}>
            <div className="title">{it.title}</div>
            <div className="meta">
              {it.container.toUpperCase()}
              {it.folder ? ' · ' + it.folder : ''}
              {it.played ? ' · Watched' : it.positionMs > 0 ? ` · Stopped at ${formatTime(it.positionMs)}` : ''}
            </div>
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
    if (v && v.currentTime > 3 && v.webkitAudioDecodedByteCount === 0 && !v.muted) {
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
        <button onClick={onBack}>← Back to the library</button>
        <h1 style={{ fontSize: '1.2rem' }}>{info?.title || ''}</h1>
      </div>
      {error && <p className="error" role="alert">{error}</p>}
      {problem && <p className="note" role="alert" data-testid="play-problem">{problem}</p>}
      {info && (
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
