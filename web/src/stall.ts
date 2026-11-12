// Notices when a film that should be playing is not, and decides when that
// is trouble worth telling the PC about.
//
// The rules are here as plain functions of what the player says about
// itself and of the time, both handed in: nothing in this file touches the
// browser, so the rules are tested without one (stall.test.mjs, run by
// `node --test web/src/stall.test.mjs` and as the first step of the
// browser test). The page looks at its player twice a second, passes what
// it sees to look(), and acts on what comes back.
//
// Two things come out of a look:
//
//   - How long the viewer has been waiting for a film that should be
//     moving. After three seconds the page says so in words; after twelve
//     it offers ways out.
//   - Whether to report trouble to the PC, which may then start the film
//     again in a lighter or an easier form. Trouble is reported when
//       * the player has been run dry for 8 seconds, all told, within a
//         minute;
//       * or it ran dry 3 separate times within a minute (each for a
//         second or more: a blink is not a stall);
//       * or one wait has lasted 12 seconds, however it began;
//       * or more than a fifth of the pictures were left out, twice
//         running (the device cannot keep up with showing them).
//     Running dry counts only while the film is meant to be playing: never
//     while it is paused or the tab is hidden, and not within 10 seconds of
//     a start, a jump, a switch of sound track or the tab being shown
//     again, when a moment's wait is normal. Only the 12-second rule looks
//     past that: a start or a jump that takes that long is trouble too.

export const RULES = {
  // How often the page looks at its player.
  everyMs: 500,
  // The viewer is told in words after this long a wait, and offered ways
  // out after this long.
  wordsMs: 3_000,
  cardMs: 12_000,
  // Nothing counts as running dry this soon after a start, a jump, a
  // switch of track or the tab being shown.
  graceMs: 10_000,
  // "Within a minute".
  windowMs: 60_000,
  starvedMs: 8_000,
  stalls: 3,
  // A run dry shorter than this adds to the time but is not counted as one
  // of the three: a player may blink without anyone noticing.
  stallAtLeastMs: 1_000,
  longWaitMs: 12_000,
  // After a report nothing more is reported for this long: the PC has
  // answered, and asking again at once would only get the same answer.
  quietMs: 60_000,
  // Pictures left out: looked at every ten seconds of playing, over at
  // least a hundred pictures.
  framesEveryMs: 10_000,
  framesAtLeast: 100,
  droppedShare: 0.2,
  droppedRuns: 2,
} as const

// What the PC can be told went wrong (see the trouble route).
export type Evidence = 'decode' | 'starved' | 'renderer'

// What the page sees when it looks at its player.
export interface Reading {
  now: number // milliseconds, from any clock that only goes forward
  // The film is meant to be moving: the player is neither paused nor at
  // its end.
  playing: boolean
  // The film has been asked for and has not begun to play yet, and the
  // browser is not waiting for the viewer to press Play.
  starting: boolean
  time: number // the player's own clock, in seconds
  // How much the player holds: 3 or more means it has something to show
  // next (HTMLMediaElement.readyState).
  ready: number
  hidden: boolean // the tab is not showing
  // How many pictures the player has been given and how many of them it
  // left out, where the browser says.
  frames?: { total: number; dropped: number }
}

export interface Trouble {
  evidence: Evidence
  // A few words for the PC's log about what was seen.
  detail: string
}

export interface Finding {
  // How long the viewer has been waiting for the film to move, 0 when it
  // moves or is not meant to.
  waitedMs: number
  // 0: nothing to say. 1: say that the film is still being got ready.
  // 2: offer ways out.
  stage: 0 | 1 | 2
  trouble: Trouble | null
}

// A stretch of running dry that counts: its part outside the grace periods.
interface Spell {
  from: number
  to: number
}

export interface Watch {
  graceUntil: number
  lastTime: number // the player's clock at the last look; NaN before the first
  waitSince: number | null
  // The present run dry: when it began, and from when it counts.
  dry: { since: number; counts: number } | null
  spells: Spell[] // earlier ones that counted
  quietUntil: number
  frames: { at: number; total: number; dropped: number } | null
  droppedRuns: number
}

// newWatch begins watching a film that has just been handed to the player.
// waitSince is when the viewer began to wait for it, if they already were:
// for the PC to answer, or for the film this one replaces.
export function newWatch(now: number, waitSince: number | null = null): Watch {
  return { graceUntil: now + RULES.graceMs, lastTime: NaN, waitSince, dry: null, spells: [], quietUntil: 0, frames: null, droppedRuns: 0 }
}

// mark notes a moment after which waiting is normal for a while: the film
// started, the viewer jumped, the sound track was switched, or the tab is
// showing again. A wait that was under way begins afresh.
export function mark(w: Watch, now: number): void {
  endDry(w, now)
  w.graceUntil = now + RULES.graceMs
  w.waitSince = null
  w.frames = null
}

// endDry closes the present run dry and keeps the part of it that counts.
function endDry(w: Watch, now: number): void {
  if (w.dry && now > w.dry.counts) w.spells.push({ from: w.dry.counts, to: now })
  w.dry = null
}

// look takes one reading of the player and says what follows from it.
export function look(w: Watch, r: Reading): Finding {
  const moved = r.time !== w.lastTime
  w.lastTime = r.time
  if (r.hidden) {
    // A hidden tab is not being watched, and browsers slow it down. Being
    // shown again counts as a start.
    mark(w, r.now)
    return { waitedMs: 0, stage: 0, trouble: null }
  }
  const stuck = r.playing && !moved
  const waiting = stuck || r.starting
  if (!waiting) w.waitSince = null
  else if (w.waitSince === null) w.waitSince = r.now
  const waitedMs = w.waitSince === null ? 0 : r.now - w.waitSince
  const stage = waitedMs >= RULES.cardMs ? 2 : waitedMs >= RULES.wordsMs ? 1 : 0

  // Running dry: meant to be moving, not moving, and nothing held to show.
  const dry = stuck && r.ready < 3
  if (!dry) endDry(w, r.now)
  else if (!w.dry) w.dry = { since: r.now, counts: Math.max(r.now, w.graceUntil) }

  let trouble = r.now < w.quietUntil ? null : starved(w, r.now)
  const left = leftOut(w, r, moved)
  if (!trouble && r.now >= w.quietUntil) trouble = left
  if (trouble) {
    // Said once. What led to it is not counted again.
    w.quietUntil = r.now + RULES.quietMs
    w.spells = []
    w.droppedRuns = 0
    if (w.dry) w.dry = { since: r.now, counts: r.now }
  }
  return { waitedMs, stage, trouble }
}

// lateChunk is for a player that says a part of the film did not begin to
// arrive in time (the page's own chunk player gives a part half a minute).
// That is running dry in the making, whether or not the player still holds
// film to show, and is reported like it: while the film is meant to be
// playing, and not on the heels of another report.
export function lateChunk(w: Watch, now: number, playing: boolean): Trouble | null {
  if (!playing || now < w.quietUntil) return null
  w.quietUntil = now + RULES.quietMs
  w.spells = []
  return { evidence: 'starved', detail: 'a part of the film did not begin to arrive within half a minute' }
}

// starved applies the three rules about running dry.
function starved(w: Watch, now: number): Trouble | null {
  const from = now - RULES.windowMs
  w.spells = w.spells.filter((s) => s.to > from)
  let total = 0
  let count = 0
  for (const s of w.spells) {
    total += s.to - Math.max(s.from, from)
    if (s.from >= from && s.to - s.from >= RULES.stallAtLeastMs) count++
  }
  if (w.dry && now > w.dry.counts) {
    total += now - w.dry.counts
    if (now - w.dry.counts >= RULES.stallAtLeastMs) count++
  }
  if (w.dry && now - w.dry.since >= RULES.longWaitMs) return { evidence: 'starved', detail: `waited ${seconds(now - w.dry.since)} s for the film to move` }
  if (total >= RULES.starvedMs) return { evidence: 'starved', detail: `ran dry for ${seconds(total)} s within a minute` }
  if (count >= RULES.stalls) return { evidence: 'starved', detail: `ran dry ${count} times within a minute` }
  return null
}

// leftOut applies the rule about pictures the player left out. Only a
// stretch of plain playing is judged: a jump or a wait leaves pictures out
// in the ordinary way, and the count starts again after it.
function leftOut(w: Watch, r: Reading, moved: boolean): Trouble | null {
  if (!r.frames || !r.playing || !moved || r.now < w.graceUntil) {
    w.frames = null
    return null
  }
  if (!w.frames) {
    w.frames = { at: r.now, ...r.frames }
    return null
  }
  if (r.now - w.frames.at < RULES.framesEveryMs) return null
  const total = r.frames.total - w.frames.total
  const dropped = r.frames.dropped - w.frames.dropped
  w.frames = { at: r.now, ...r.frames }
  if (total < RULES.framesAtLeast) return null // too few to judge by
  if (dropped / total <= RULES.droppedShare) {
    w.droppedRuns = 0
    return null
  }
  w.droppedRuns++
  if (w.droppedRuns < RULES.droppedRuns) return null
  return { evidence: 'decode', detail: `left out ${Math.round((dropped / total) * 100)}% of the pictures, ${RULES.droppedRuns} times running` }
}

function seconds(ms: number): string {
  return (ms / 1000).toFixed(1).replace(/\.0$/, '')
}

// What the page gets from its player for a Reading. Kept apart from the
// rules so that they need no browser.
export interface PlayerLike {
  paused: boolean
  ended: boolean
  currentTime: number
  duration: number
  readyState: number
  getVideoPlaybackQuality?: () => { totalVideoFrames: number; droppedVideoFrames: number }
}

// reading puts together what look() wants from a video element. starting is
// for the caller to say (see Reading.starting); a film that has not begun
// counts as starting only while it is still paused, because once the
// browser has agreed to play, "playing" says the same and more.
//
// A player that has come to rest within a quarter of a second of the
// film's end is taken to be at the end, whether or not it says so: some
// stop a few pictures short, and that is not a film in trouble.
export function reading(v: PlayerLike, now: number, starting: boolean, hidden: boolean): Reading {
  const atEnd = v.ended || (isFinite(v.duration) && v.duration > 0 && v.currentTime >= v.duration - 0.25)
  const playing = !v.paused && !atEnd
  let frames: Reading['frames']
  if (typeof v.getVideoPlaybackQuality === 'function') {
    const q = v.getVideoPlaybackQuality()
    if (q && q.totalVideoFrames >= 0) frames = { total: q.totalVideoFrames, dropped: q.droppedVideoFrames }
  }
  return { now, playing, starting: starting && !playing, time: v.currentTime, ready: v.readyState, hidden, frames }
}
