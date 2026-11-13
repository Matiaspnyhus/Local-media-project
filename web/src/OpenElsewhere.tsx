import { useEffect, useId, useRef, useState, type KeyboardEvent } from 'react'
import { flushSync } from 'react-dom'
import { api, vlcFile, type ApiError, type Handoff } from './api'

const DAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

// clockInWords says a moment the way a person would look for it on a clock
// and a calendar: "21:30 on Friday" (or "9:30 PM on Friday", as this device
// writes its times). The day is there because the moment can be tomorrow.
export function clockInWords(at: Date): string {
  return `${at.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })} on ${DAYS[at.getDay()]}`
}

// After a film has been opened in the PC's video player, the button does
// nothing more for this long. A player can take a few seconds to appear,
// and a second press in that time would only open the film twice. (The PC
// itself refuses to open the same film again for as long.)
const HOLD_MS = 5000
const JUST_OPENED =
  "This film was opened in this PC's video player a moment ago. If no player appeared, wait a few seconds and press the button again."

// The address this page was opened at says whether it can be the browser
// on the PC itself: no other device can reach the PC as "localhost". The
// PC has the last word (see press).
const mayBeThePC = () => ['localhost', '127.0.0.1', '[::1]'].includes(window.location.hostname)

// A key that is held down repeats, about thirty times a second. One press
// is one action, however long the key is held.
const once = (e: KeyboardEvent) => {
  if (e.repeat && (e.key === 'Enter' || e.key === ' ')) e.preventDefault()
}

// OpenElsewhere is the way out for a film this browser cannot play well: a
// real video player plays the original file, at full quality.
//
// In the browser on the PC that runs Local Media, the button opens the film
// in that PC's own video player. On any other device the PC cannot start a
// player, so the button offers two things to take to one: a small file that
// VLC opens, and a link to copy.
//
// The button itself is never removed or switched off while it is in use,
// so the keyboard's place on the page is not lost; what happened is said
// beside it.
export function OpenElsewhere({
  id,
  position,
  onLeave,
  onSignedOut,
}: {
  id: string
  position: () => number // where the film is now, in seconds
  onLeave: () => void // the film is on its way to another player: stop it here
  onSignedOut: () => void
}) {
  const [said, setSaid] = useState('') // what happened, when it went well
  const [problem, setProblem] = useState('')
  const [offer, setOffer] = useState<Handoff | null>(null)
  const [ends, setEnds] = useState(0) // when the offer's link stops working, by this device's clock
  const [at, setAt] = useState(0) // where the VLC file starts the film, in whole seconds
  // Whether this is the browser on the PC. On another device that is known
  // from the start; on the PC's own address, once the PC has been asked.
  const [onThisPC, setOnThisPC] = useState<boolean | null>(() => (mayBeThePC() ? null : false))
  const busy = useRef(false)
  const openedAt = useRef(0)
  const linkId = useId()

  const now = () => Math.max(0, Math.floor(position()))

  // The link in an offer runs out. When it has, the offer is put away, so
  // that nothing on the page hands out a link that no longer works.
  useEffect(() => {
    if (!offer) return
    const timer = window.setTimeout(() => {
      setOffer(null)
      setProblem('')
      setSaid('That link has run out. Press the button again for a new one.')
    }, Math.max(0, ends - Date.now()))
    return () => window.clearTimeout(timer)
  }, [offer, ends])

  const show = (h: Handoff, seconds: number) => {
    setAt(seconds)
    setEnds(Date.now() + h.validSeconds * 1000)
    setOffer(h)
  }

  const press = () => {
    if (busy.current) return
    // On another device a second press puts the offer away again.
    if (offer && !problem) {
      setOffer(null)
      return
    }
    if (Date.now() - openedAt.current < HOLD_MS) {
      setSaid(JUST_OPENED)
      return
    }
    busy.current = true
    setProblem('')
    setSaid('One moment…')
    const seconds = now()
    api
      .handoff(id)
      .then((h) => {
        setOnThisPC(h.thisPC)
        if (!h.thisPC) {
          show(h, seconds)
          setSaid('')
          return
        }
        return api.openLocal(id).then(
          () => {
            openedAt.current = Date.now()
            setOffer(null)
            // The PC's player is handed the film's file and nothing else,
            // so it cannot be told where the film was on this page.
            setSaid("Opened in this PC's video player. The film starts from the beginning there.")
            onLeave()
          },
          (e: ApiError) => {
            if (e.status === 401) throw e
            if (e.status === 429) {
              // The PC was asked a moment ago and is not asked twice.
              // Nothing is wrong, so there is nothing else to offer.
              setOffer(null)
              setSaid(e.message)
              return
            }
            // The PC has no player for this file, or could not start it.
            // The file for VLC and the link are still worth offering.
            setSaid('')
            setProblem(e.message)
            show(h, seconds)
          },
        )
      })
      .catch((e: ApiError) => {
        setSaid('')
        if (e.status === 401) onSignedOut()
        else setProblem(e.message)
      })
      .then(() => {
        busy.current = false
      })
  }

  // The file is asked for where the film is at the moment of the click, not
  // where it was when the offer opened. The address has to be on the page
  // before the browser follows it, hence flushSync.
  const download = () => {
    flushSync(() => setAt(now()))
    onLeave()
  }

  // The download looks like a button, so it answers to the keys a button
  // answers to. A link by itself only follows Enter; Space would scroll the
  // page.
  const downloadKey = (e: KeyboardEvent<HTMLAnchorElement>) => {
    once(e)
    if (e.key === ' ') {
      e.preventDefault()
      if (!e.repeat) e.currentTarget.click()
    }
  }

  // Reaching the link, by keyboard, mouse or finger, selects all of it,
  // ready to copy. (setSelectionRange is for browsers on phones, where
  // select alone has not always been enough.)
  const selectAll = (el: HTMLTextAreaElement) => {
    el.select()
    el.setSelectionRange(0, el.value.length)
  }

  return (
    <div className="elsewhere" data-testid="elsewhere">
      <button
        type="button"
        data-testid="open-elsewhere"
        aria-expanded={onThisPC === false ? !!offer : undefined}
        onKeyDown={once}
        onClick={press}
      >
        Open in another player
      </button>
      <div role="status" data-testid="elsewhere-said">
        {said && <p>{said}</p>}
      </div>
      {problem && (
        <p className="error" role="alert" data-testid="elsewhere-problem">
          {problem}
        </p>
      )}
      {offer && (
        <div className="elsewhere-offer" data-testid="elsewhere-offer">
          <p data-testid="elsewhere-about">
            {onThisPC
              ? 'VLC is a free video player that plays almost anything. With VLC on this PC, press the button again, or download the file below and open it with VLC. The link is for a video player on another device at home.'
              : 'A video player such as VLC, which is free, can play this film straight from the PC: the original file, at full quality.'}
          </p>
          <p>
            <a
              className="button"
              href={vlcFile(id, at, offer.expiresAt)}
              download
              data-testid="elsewhere-vlc"
              onKeyDown={downloadKey}
              onClick={download}
            >
              Download a file for VLC
            </a>
          </p>
          <p className="dim">
            Open the downloaded file with VLC{at > 0 ? ', and the film starts where you left off' : ''}.
          </p>
          <label htmlFor={linkId}>Or copy this link into any player that can open a network address</label>
          <textarea
            id={linkId}
            className="link"
            readOnly
            rows={5}
            value={offer.url}
            data-testid="elsewhere-link"
            onFocus={(e) => selectAll(e.currentTarget)}
            onClick={(e) => selectAll(e.currentTarget)}
          />
          <p className="dim" data-testid="elsewhere-terms">
            The link and the downloaded file play this one film without signing in. Both stop working at{' '}
            {clockInWords(new Date(ends))}, or as soon as you sign out. Until then, anyone who has them can watch the
            film with them.
          </p>
        </div>
      )}
    </div>
  )
}
