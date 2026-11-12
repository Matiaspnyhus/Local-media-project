// Tests of the rules in stall.ts, which need no browser:
//
//   node --test web/src/stall.test.mjs
//
// The browser test (e2e/run.mjs) runs this file as its first step, so it is
// run wherever that is. It uses nothing but Node itself. A Node that reads
// TypeScript (22.18 and later) loads stall.ts as it is; an older one gets it
// through the TypeScript compiler the web app is built with, if that has
// been installed (npm ci in web/).
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))

async function loadRules() {
  try {
    return await import('./stall.ts')
  } catch (err) {
    if (err?.code !== 'ERR_UNKNOWN_FILE_EXTENSION') throw err
    let ts
    try {
      ts = createRequire(import.meta.url)(join(here, '..', 'node_modules', 'typescript'))
    } catch {
      throw new Error('this Node cannot read TypeScript and the web app\'s compiler is not installed: run "npm ci" in web/, or use Node 22.18 or later')
    }
    const js = ts.transpileModule(readFileSync(join(here, 'stall.ts'), 'utf8'), { compilerOptions: { module: 'ESNext', target: 'ES2020' } }).outputText
    return await import('data:text/javascript;base64,' + Buffer.from(js).toString('base64'))
  }
}

const { RULES, newWatch, mark, look, lateChunk, reading } = await loadRules()

// A film as the page would see it, looked at twice a second. The test says
// what the player is doing for how long; what the looks found is kept.
function film(startAt = 0) {
  const f = {
    now: startAt,
    time: 0,
    watch: newWatch(startAt),
    found: [],
    frames: undefined,
    // Each takes seconds and looks at the player all the way through.
    plays(s, extra = {}) {
      return f.goes(s, { playing: true, moving: true, ready: 4, ...extra })
    },
    runsDry(s, extra = {}) {
      return f.goes(s, { playing: true, moving: false, ready: 2, ...extra })
    },
    paused(s) {
      return f.goes(s, { playing: false, moving: false, ready: 4 })
    },
    starts(s) {
      return f.goes(s, { playing: false, starting: true, moving: false, ready: 0 })
    },
    hidden(s) {
      return f.goes(s, { playing: true, moving: true, ready: 4, hidden: true })
    },
    goes(s, how) {
      for (let i = 0; i < Math.round((s * 1000) / RULES.everyMs); i++) {
        f.now += RULES.everyMs
        if (how.moving) f.time += RULES.everyMs / 1000
        if (how.frames) f.frames = how.frames(f.frames || { total: 0, dropped: 0 })
        const got = look(f.watch, { now: f.now, playing: !!how.playing, starting: !!how.starting, time: f.time, ready: how.ready, hidden: !!how.hidden, frames: f.frames })
        f.found.push({ at: f.now - startAt, ...got })
      }
      return f
    },
    jumps() {
      f.time += 600
      mark(f.watch, f.now)
      return f
    },
    // When trouble was reported, in seconds since the start, with what.
    reports() {
      return f.found.filter((x) => x.trouble).map((x) => `${x.at / 1000} ${x.trouble.evidence}`)
    },
    last() {
      return f.found[f.found.length - 1]
    },
  }
  return f
}

test('a film that simply plays is left alone', () => {
  const f = film().starts(2).plays(600)
  assert.deepEqual(f.reports(), [])
  assert.equal(f.last().stage, 0)
})

test('eight seconds run dry in the middle of a film are reported, not seven and a half', () => {
  const short = film().plays(30).runsDry(7.5).plays(30)
  assert.deepEqual(short.reports(), [])
  // The first look of a run dry is where it is found, so it has lasted
  // eight seconds at the look half a second after the eighth second.
  const long = film().plays(30).runsDry(8.5)
  assert.deepEqual(long.reports(), ['38.5 starved'])
  assert.match(long.last().trouble.detail, /ran dry for 8 s within a minute/)
})

test('short stalls add up within a minute, and are forgotten after it', () => {
  // 3 + 3 + 2.5 seconds within a minute: eight seconds all told.
  const close = film().plays(20).runsDry(3.5).plays(5).runsDry(3.5).plays(5).runsDry(3)
  assert.equal(close.reports().length, 1)
  assert.match(close.reports()[0], /starved/)
  // The same stalls more than a minute apart are each a hiccup.
  const apart = film().plays(20).runsDry(3.5).plays(70).runsDry(3.5).plays(70).runsDry(3)
  assert.deepEqual(apart.reports(), [])
})

test('three separate stalls within a minute are reported, short as they are', () => {
  const f = film().plays(20).runsDry(1).plays(5).runsDry(1).plays(5)
  assert.deepEqual(f.reports(), [])
  // The third counts once it has lasted a second.
  f.runsDry(1)
  assert.deepEqual(f.reports(), [])
  f.runsDry(0.5)
  assert.equal(f.reports().length, 1)
  assert.match(f.found.find((x) => x.trouble).trouble.detail, /ran dry 3 times within a minute/)
  // Three in three minutes are not.
  const apart = film().plays(20).runsDry(1).plays(80).runsDry(1).plays(80).runsDry(1).plays(20)
  assert.deepEqual(apart.reports(), [])
  // Nor are blinks: a clock that stood still between two looks, and no more.
  const blinks = film().plays(20)
  for (let i = 0; i < 10; i++) blinks.runsDry(0.5).plays(4)
  assert.deepEqual(blinks.reports(), [])
})

test('a moment of waiting after a jump is normal; twelve seconds of it is not', () => {
  const fine = film().plays(30).jumps().runsDry(9).plays(30)
  assert.deepEqual(fine.reports(), [])
  // (The look that follows a jump sees the clock in its new place; the wait
  // is found at the look after that, half a second on.)
  const stuck = film().plays(30).jumps().runsDry(12.5)
  assert.deepEqual(stuck.reports(), [])
  stuck.runsDry(0.5)
  assert.deepEqual(stuck.reports(), ['43 starved'])
  assert.match(stuck.last().trouble.detail, /waited 12 s for the film to move/)
})

test('a start that takes long is reported only once the browser has agreed to play and twelve seconds have gone', () => {
  // Waiting for the film's first bytes: the viewer is told, the PC is not.
  const asking = film().starts(40)
  assert.deepEqual(asking.reports(), [])
  assert.equal(asking.last().stage, 2)
  // The browser plays, and nothing comes.
  const dry = film().starts(1).runsDry(12.5, { ready: 1 })
  assert.deepEqual(dry.reports(), ['13.5 starved'])
})

test('a stall that begins just after a start counts from the end of the ten seconds', () => {
  // Plays from second 1, runs dry from second 7: the ten seconds of grace
  // end at second 10, and eight seconds counted from there end at 18.
  const f = film().starts(1).plays(6).runsDry(10.5)
  assert.deepEqual(f.reports(), [])
  f.runsDry(1)
  assert.deepEqual(f.reports(), ['18 starved'])
})

test('nothing is reported while the film is paused or the tab is hidden, nor just after it is shown again', () => {
  assert.deepEqual(film().plays(30).paused(300).plays(30).reports(), [])
  const f = film().plays(30).hidden(300)
  assert.deepEqual(f.reports(), [])
  assert.equal(f.last().stage, 0)
  // Shown again: a player that has to catch up gets its ten seconds.
  f.runsDry(9).plays(30)
  assert.deepEqual(f.reports(), [])
})

test('a player that holds film but does not move is the viewer\'s wait, not a film run dry', () => {
  // A clock that stands still although there is film to show is not for
  // want of film, so it is never reported as starved. The viewer is still
  // told, and offered ways out.
  const f = film().plays(30).goes(40, { playing: true, moving: false, ready: 4 })
  assert.deepEqual(f.reports(), [])
  assert.equal(f.last().stage, 2)
})

test('the viewer is told after three seconds of waiting and offered ways out after twelve', () => {
  const f = film().starts(2.5)
  assert.equal(f.last().stage, 0)
  f.starts(1)
  assert.equal(f.last().stage, 1)
  f.starts(8)
  assert.equal(f.last().stage, 1)
  f.starts(1)
  assert.equal(f.last().stage, 2)
  f.plays(1)
  assert.equal(f.last().stage, 0)
  assert.equal(f.last().waitedMs, 0)
  // The same for a film that stops in the middle.
  f.plays(30).runsDry(3.5)
  assert.equal(f.last().stage, 1)
})

test('pictures left out: more than a fifth, twice running, is reported as trouble showing the picture', () => {
  const smooth = (q) => ({ total: q.total + 12, dropped: q.dropped })
  const rough = (q) => ({ total: q.total + 12, dropped: q.dropped + 4 }) // a third
  const once = film().plays(20, { frames: smooth }).plays(10.5, { frames: rough }).plays(40, { frames: smooth })
  assert.deepEqual(once.reports(), [])
  const twice = film().plays(20, { frames: smooth }).plays(30, { frames: rough })
  assert.equal(twice.reports().length, 1)
  assert.match(twice.reports()[0], /decode/)
  assert.match(twice.found.find((x) => x.trouble).trouble.detail, /left out 33% of the pictures, 2 times running/)
  // A fifth exactly is not "more than a fifth".
  const fifth = (q) => ({ total: q.total + 10, dropped: q.dropped + 2 })
  assert.deepEqual(film().plays(120, { frames: fifth }).reports(), [])
  // Too few pictures to judge by (a film that is nearly a slide show).
  const few = (q) => ({ total: q.total + 1, dropped: q.dropped + 1 })
  assert.deepEqual(film().plays(120, { frames: few }).reports(), [])
})

test('pictures left out around a jump are not held against the device', () => {
  const smooth = (q) => ({ total: q.total + 12, dropped: q.dropped })
  const f = film().plays(25, { frames: smooth })
  // A jump throws pictures away wholesale.
  f.frames = { total: f.frames.total + 500, dropped: f.frames.dropped + 400 }
  f.jumps().plays(60, { frames: smooth })
  assert.deepEqual(f.reports(), [])
})

test('after a report nothing more is reported for a minute', () => {
  const f = film().plays(30).runsDry(100)
  assert.deepEqual(f.reports(), ['38.5 starved', '98.5 starved'])
})

test('a part of the film that does not arrive in time is reported while the film plays, once', () => {
  const w = newWatch(0)
  assert.equal(lateChunk(w, 40_000, false), null, 'not while the film is paused')
  assert.equal(lateChunk(w, 40_000, true).evidence, 'starved')
  assert.equal(lateChunk(w, 70_000, true), null, 'not again within a minute')
  assert.equal(lateChunk(w, 101_000, true).evidence, 'starved')
  // And not on the heels of a report the watch made itself.
  const f = film().plays(30).runsDry(8.5)
  assert.equal(f.reports().length, 1)
  assert.equal(lateChunk(f.watch, f.now + 5000, true), null)
})

test('a wait that was already under way goes on counting when the film is handed to the player', () => {
  // Five seconds for the PC to answer, then the player takes over.
  const asking = film().starts(5)
  const w = newWatch(asking.now, asking.watch.waitSince)
  const got = look(w, { now: asking.now + 500, playing: false, starting: true, time: 0, ready: 0, hidden: false })
  assert.equal(got.waitedMs, 5000)
  assert.equal(got.stage, 1)
})

test('what the page reads off its player', () => {
  const v = { paused: false, ended: false, currentTime: 12, duration: 100, readyState: 4 }
  assert.deepEqual(reading(v, 5, false, false), { now: 5, playing: true, starting: false, time: 12, ready: 4, hidden: false, frames: undefined })
  // A film that has not begun counts as starting only until the browser plays.
  assert.equal(reading({ ...v, paused: true }, 5, true, false).starting, true)
  assert.equal(reading(v, 5, true, false).starting, false)
  // At rest at the end, or a few pictures short of it, is not playing.
  assert.equal(reading({ ...v, ended: true }, 5, false, false).playing, false)
  assert.equal(reading({ ...v, currentTime: 99.9 }, 5, false, false).playing, false)
  assert.equal(reading({ ...v, duration: NaN }, 5, false, false).playing, true)
  const q = reading({ ...v, getVideoPlaybackQuality: () => ({ totalVideoFrames: 300, droppedVideoFrames: 7 }) }, 5, false, false)
  assert.deepEqual(q.frames, { total: 300, dropped: 7 })
})
