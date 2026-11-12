import { useCallback, useEffect, useId, useRef, useState } from 'react'
import { api, type ApiError, type Device, type Devices, type Lesson } from './api'

// When a film has trouble playing, the PC changes how it sends films to
// that device and remembers why (a "lesson"). These cards list what is
// remembered, in the PC's own sentences, and let it be forgotten: each
// viewer for the device in their hand, the owner for every device and for
// what the PC has learned about its own speed.

// until says when a lesson runs out, in words: the day, and for one that
// runs out within two days the time as well.
export function until(expiresAt: string, now = Date.now()): string {
  const t = new Date(expiresAt)
  if (isNaN(t.getTime())) return ''
  if (t.getTime() - now < 48 * 3600 * 1000)
    return t.toLocaleString(undefined, { weekday: 'long', hour: 'numeric', minute: '2-digit' })
  return t.toLocaleDateString(undefined, { day: 'numeric', month: 'long' })
}

// ago says how long since a device was last used, to the day.
export function ago(lastSeenAt: string, now = Date.now()): string {
  const t = new Date(lastSeenAt).getTime()
  if (isNaN(t)) return ''
  const days = Math.floor((now - t) / (24 * 3600 * 1000))
  if (days <= 0) return 'today'
  if (days === 1) return 'yesterday'
  return `${days} days ago`
}

// LessonList shows lessons with a Forget button each. The sentence beside
// a button is what a screen reader says the button is about.
function LessonList({ lessons, onForget, testid }: { lessons: Lesson[]; onForget: (l: Lesson) => void; testid: string }) {
  const base = useId()
  return (
    <ul className="lessons" data-testid={testid}>
      {lessons.map((l, i) => (
        <li key={l.id}>
          <span id={`${base}-${i}`}>
            {l.text} <span className="dim">Remembered until {until(l.expiresAt)}.</span>
          </span>
          <button type="button" aria-describedby={`${base}-${i}`} onClick={() => onForget(l)}>
            Forget
          </button>
        </li>
      ))}
    </ul>
  )
}

// Learned is what the settings show about lessons. changed is called after
// something was forgotten, so that lists elsewhere on the page can follow.
export function Learned({ owner, onSignedOut }: { owner: boolean; onSignedOut: () => void }) {
  const [mine, setMine] = useState<Device | null>(null)
  const [all, setAll] = useState<Devices | null>(null)
  const [said, setSaid] = useState('')
  const [problem, setProblem] = useState('')
  // Forgetting takes the pressed button off the page. The keyboard's place
  // is moved to the line that says what happened, so that it is not lost.
  const saidRef = useRef<HTMLParagraphElement | null>(null)

  const load = useCallback(() => {
    const fail = (e: ApiError) => (e.status === 401 ? onSignedOut() : setProblem(e.message))
    api.device().then(setMine).catch(fail)
    if (owner) api.devices().then(setAll).catch(fail)
  }, [owner, onSignedOut])
  useEffect(load, [load])

  const forgot = (what: Promise<unknown>) => {
    setProblem('')
    what
      .then(() => {
        setSaid('Forgotten. The next film is sent as if this had not happened.')
        saidRef.current?.focus()
      })
      .catch((e: ApiError) => {
        // Someone else forgot it first, or it ran out a moment ago.
        if (e.status === 404) setSaid('That was already gone.')
        else if (e.status === 401) onSignedOut()
        else setProblem(e.message)
        saidRef.current?.focus()
      })
      .then(load)
  }

  if (!mine) return problem ? <p className="error" role="alert">{problem}</p> : null
  return (
    <>
      <div className="card" data-testid="learned">
        <h2 style={{ marginTop: 0 }}>Learned about this device</h2>
        <p className="dim">
          This device is known to the app as: {mine.label}. When a film has trouble playing here, the app changes how it
          sends films to this device and notes below what it changed and why. Each note is dropped by itself after a
          while. "Forget" drops it now, and the next film is tried the ordinary way again.
        </p>
        {mine.lessons.length === 0 ? (
          <p data-testid="learned-none">Nothing has been learned about this device.</p>
        ) : (
          <LessonList lessons={mine.lessons} testid="learned-list" onForget={(l) => forgot(api.forgetLesson(l.id))} />
        )}
        <p role="status" tabIndex={-1} ref={saidRef} data-testid="learned-said" className="said">
          {said}
        </p>
        {problem && <p className="error" role="alert">{problem}</p>}
      </div>
      {owner && all && (
        <div className="card" data-testid="devices">
          <h2 style={{ marginTop: 0 }}>All devices</h2>
          <p className="dim">
            Every phone, tablet, TV and computer that has signed in, by what kind of device it is. The app does not know
            their names. A device that is not used for more than a year is dropped from this list.
          </p>
          {all.devices.map((d) => (
            <section key={d.id} className="device" data-testid="device">
              <h3>
                {d.label}
                {d.thisDevice ? ' (this device)' : ''}
              </h3>
              <p className="dim">Last used {ago(d.lastSeenAt)}.</p>
              {d.lessons.length === 0 ? (
                <p className="dim">Nothing learned.</p>
              ) : (
                <LessonList lessons={d.lessons} testid="device-lessons" onForget={(l) => forgot(api.forgetDeviceLesson(d.id, l.id))} />
              )}
            </section>
          ))}
          <section className="device" data-testid="pc-lessons">
            <h3>This PC</h3>
            {all.pc.lessons.length === 0 ? (
              <p className="dim">Nothing has been learned about how fast this PC converts films.</p>
            ) : (
              <LessonList lessons={all.pc.lessons} testid="pc-lesson-list" onForget={(l) => forgot(api.forgetDeviceLesson('pc', l.id))} />
            )}
          </section>
        </div>
      )}
    </>
  )
}
