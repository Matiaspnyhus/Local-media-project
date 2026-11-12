import { useId, useRef, useState } from 'react'
import { flushSync } from 'react-dom'
import { api, vlcFile, type ApiError, type Handoff } from './api'

// lifeInWords says how long a link lasts ("12 hours"), from the number of
// seconds the PC gives.
export function lifeInWords(seconds: number): string {
  const hours = seconds / 3600
  if (hours >= 1 && Number.isInteger(hours)) return hours === 1 ? '1 hour' : `${hours} hours`
  if (hours >= 2) return `about ${Math.floor(hours)} hours`
  const minutes = Math.max(1, Math.round(seconds / 60))
  return minutes === 1 ? '1 minute' : `${minutes} minutes`
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
  const [at, setAt] = useState(0) // where the VLC file starts the film, in whole seconds
  const [onThisPC, setOnThisPC] = useState<boolean | null>(null) // not known until the PC has been asked once
  const busy = useRef(false)
  const linkId = useId()

  const now = () => Math.max(0, Math.floor(position()))

  const press = () => {
    if (busy.current) return
    // On another device a second press puts the offer away again.
    if (offer && !problem) {
      setOffer(null)
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
          setAt(seconds)
          setOffer(h)
          setSaid('')
          return
        }
        return api.openLocal(id).then(
          () => {
            setOffer(null)
            setSaid("Opened in this PC's video player.")
            onLeave()
          },
          (e: ApiError) => {
            if (e.status === 401) throw e
            // The PC has no player for this file, or could not start it.
            // The file for VLC and the link are still worth offering.
            setSaid('')
            setProblem(e.message)
            setAt(seconds)
            setOffer(h)
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

  // Reaching the link, by keyboard, mouse or finger, selects all of it,
  // ready to copy. (setSelectionRange is for browsers on phones, where
  // select alone has not always been enough.)
  const selectAll = (el: HTMLTextAreaElement) => {
    el.select()
    el.setSelectionRange(0, el.value.length)
  }

  return (
    <div className="elsewhere" data-testid="elsewhere">
      <button type="button" data-testid="open-elsewhere" aria-expanded={onThisPC === false ? !!offer : undefined} onClick={press}>
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
          <p>
            A video player such as VLC, which is free, can play this film straight from the PC: the original file, at full
            quality.
          </p>
          <p>
            <a className="button" href={vlcFile(id, at)} download data-testid="elsewhere-vlc" onClick={download}>
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
            This link plays this one film without signing in, for {lifeInWords(offer.validSeconds)}, and stops working
            when you sign out. Until then, anyone who has the link can watch the film with it.
          </p>
        </div>
      )}
    </div>
  )
}
