// End-to-end check of M0, driven with the keyboard only, against the real
// program. The owner's journey: set up on the PC, then sign in from another
// device over plain HTTP, play a file, leave, and resume.
//
//   node e2e/run.mjs            (Chromium)
//   E2E_BROWSER=webkit node e2e/run.mjs
//
// Needs the program built at ./localmedia (or LOCALMEDIA_BIN).
import { spawn, spawnSync } from 'node:child_process'
import { mkdtempSync, cpSync, rmSync, existsSync, mkdirSync, copyFileSync, writeFileSync, readFileSync } from 'node:fs'
import { tmpdir, networkInterfaces } from 'node:os'
import { join, dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import assert from 'node:assert/strict'
import { createRequire } from 'node:module'

const require = createRequire(import.meta.url)
const playwright = require('playwright')
const here = dirname(fileURLToPath(import.meta.url))
const exe = process.platform === 'win32' ? 'localmedia.exe' : 'localmedia'
const bin = resolve(process.env.LOCALMEDIA_BIN || join(here, '..', exe))
const browserName = process.env.E2E_BROWSER || 'chromium'
const port = Number(process.env.E2E_PORT) || 18177 // E2E_PORT lets two runs share a machine
// Safari's engine on a Mac moves focus to buttons with Option+Tab, not Tab.
const TAB = browserName === 'webkit' ? 'Alt+Tab' : 'Tab'

function lanAddress() {
  for (const list of Object.values(networkInterfaces()))
    for (const a of list || []) if (a.family === 'IPv4' && !a.internal) return a.address
  return null
}

const steps = []
async function step(name, fn) {
  const t = Date.now()
  await fn()
  steps.push(name)
  console.log(`  ok  ${name} (${Date.now() - t} ms)`)
}

async function pressUntilFocused(page, selector, key = TAB, max = 80) {
  for (let i = 0; i < max; i++) {
    if (await page.evaluate((s) => document.activeElement === document.querySelector(s), selector)) return
    await page.keyboard.press(key)
  }
  throw new Error(`keyboard could not reach ${selector}`)
}

async function focusByText(page, text, max = 80) {
  for (let i = 0; i < max; i++) {
    const t = await page.evaluate(() => {
      const el = document.activeElement
      return el && (el.tagName === 'BUTTON' || el.tagName === 'A') ? el.textContent || '' : ''
    })
    if (t.includes(text)) return
    await page.keyboard.press(TAB)
  }
  throw new Error(`keyboard could not reach "${text}"`)
}

async function waitPlaying(page, beyond) {
  // Autoplay with sound may be refused when nobody has touched the page;
  // muted playback is always allowed, and is enough for this check.
  await page.waitForSelector('video')
  await page.waitForFunction(() => document.querySelector('video').readyState >= 1, null, { timeout: 15000 })
  await page.evaluate(() => {
    const v = document.querySelector('video')
    if (v.paused) {
      v.muted = true
      v.play().catch(() => {})
    }
  })
  await page.waitForFunction((b) => document.querySelector('video').currentTime > b, beyond, { timeout: 20000 })
}

// Waits until the film is playing on beyond a given second. Used where the
// film is handed to the player afresh while it is open (a change of sound,
// a jump): the nudge that waitPlaying gives once is given until it takes.
async function playsBeyond(page, beyond, why) {
  await page.waitForFunction((b) => {
    const v = document.querySelector('video')
    if (!v) return false
    if (v.paused && v.readyState >= 1) {
      v.muted = true
      v.play().catch(() => {})
    }
    return !v.seeking && v.currentTime > b
  }, beyond, { timeout: 20000, polling: 200 }).catch(async () => {
    const t = await page.evaluate(() => document.querySelector('video')?.currentTime)
    assert.fail(`${why}: after 20 seconds the film is at ${t} s, not playing beyond ${beyond} s`)
  })
}

// Keeps a record of what the player goes through and what is asked of the
// PC, from now until the function it returns is called; that function gives
// the record back, ready to print. It is for a step that took far longer in
// one run than in another, with nothing to say where the time went. Every
// time in it is in milliseconds since the record was started.
//
// The player's part reads "seeking+12 seeked+40": the events of the video
// element and when they came. The PC's part reads "new2/seg-10.m4s+659..1155":
// what was asked for, when, and when the answer was complete ("..?" if it
// never was, "(given up)" if the player dropped the request, "(409)" for an
// answer other than a plain yes). "open" is the request a pick of sound
// sends. "stop" is the request to stop converting a film, with only the
// time it was sent: it is sent so as to outlive the page, and a test is not
// told when such a request is answered. "old" is the film that was already
// playing, "new1", "new2" and so on the films the PC opened for the first
// "open", the second and so on.
async function record(page) {
  const t0 = Date.now()
  const asked = []
  const answers = []
  const onRequest = (r) => {
    const m = /\/hls\/([0-9a-f]+)\/([^/]+)$/.exec(r.url())
    if (m) asked.push({ r, at: Date.now() - t0, film: m[1], what: m[2] })
    else if (/\/api\/items\/[^/]+\/play$/.test(r.url())) asked.push({ r, at: Date.now() - t0, film: '', what: 'open' })
  }
  const answered = async (r, givenUp) => {
    const e = asked.find((x) => x.r === r)
    if (!e) return
    e.ms = Date.now() - t0 - e.at
    e.givenUp = givenUp
    const res = givenUp ? null : await r.response().catch(() => null)
    e.status = res ? res.status() : 0
    if (e.what === 'open' && res) e.opened = (await res.json().catch(() => null))?.hls
  }
  const onFinished = (r) => answers.push(answered(r, false).catch(() => {}))
  const onFailed = (r) => answers.push(answered(r, true).catch(() => {}))
  page.on('request', onRequest)
  page.on('requestfinished', onFinished)
  page.on('requestfailed', onFailed)
  await page.evaluate(() => {
    const from = performance.now()
    const went = []
    const stop = new AbortController()
    for (const name of ['emptied', 'loadstart', 'loadedmetadata', 'loadeddata', 'canplay', 'seeking', 'seeked', 'waiting', 'stalled', 'playing', 'pause', 'error'])
      document.querySelector('video').addEventListener(name, () => went.push(`${name}+${Math.round(performance.now() - from)}`), { signal: stop.signal })
    window.playerRecord = () => {
      stop.abort()
      return went
    }
  })
  return async () => {
    page.off('request', onRequest)
    page.off('requestfinished', onFinished)
    page.off('requestfailed', onFailed)
    // The record is asked for when a step has failed, too, and the page may
    // then be past answering: nothing here may wait for it without end, or
    // fail in the place of what really went wrong.
    const within = (ms, promise, otherwise) => Promise.race([promise.catch(() => otherwise), new Promise((r) => setTimeout(() => r(otherwise), ms))])
    await within(3000, Promise.all(answers))
    const went = await within(5000, page.evaluate(() => window.playerRecord()), ['(the page did not answer)'])
    const opens = asked.filter((e) => e.what === 'open')
    const film = (e) => {
      const n = opens.findIndex((o) => o.opened === e.film)
      return n < 0 ? 'old' : `new${n + 1}`
    }
    const one = (e) => {
      if (e.what === 'stop') return `stop:${film(e)}+${e.at}`
      const what = e.what === 'open' ? 'open' : `${film(e)}/${e.what}`
      const until = e.ms === undefined ? '..?' : `..${e.at + e.ms}`
      return `${what}+${e.at}${until}${e.givenUp ? '(given up)' : e.status && e.status !== 200 ? `(${e.status})` : ''}`
    }
    const lists = asked.filter((e) => e.what === 'index.m3u8')
    return {
      lists: lists.length,
      films: new Set(lists.map((e) => e.film)).size,
      player: went.slice(0, 90).join(' ') || '(nothing)',
      asked: asked.slice(0, 90).map(one).join(' ') || '(nothing)',
    }
  }
}

// Whether the keyboard is on the element a selector names.
const hasFocus = (p, selector) => p.evaluate((s) => document.activeElement === document.querySelector(s), selector)

// What the PC has learned about the device a page runs on, and making it
// forget all of it: for steps that provoke trouble, so that what one step
// teaches the PC does not change how the next step's films are sent.
const learned = (p) => p.evaluate(async () => (await (await fetch('/api/device')).json()).lessons)
const forgetAll = (p) => p.evaluate(async () => {
  for (const l of (await (await fetch('/api/device')).json()).lessons)
    await fetch(`/api/device/lessons/${l.id}`, { method: 'DELETE', headers: { 'X-Requested-With': 'localmedia', 'Content-Type': 'application/json' }, body: '{}' })
})
// Puts a film back to its beginning, whatever an earlier step left it at.
const fromTheStart = (p, title) => p.evaluate(async (t) => {
  const it = (await (await fetch('/api/movies')).json()).items.find((x) => x.title === t)
  await fetch(`/api/items/${it.id}/progress`, { method: 'PUT', headers: { 'X-Requested-With': 'localmedia', 'Content-Type': 'application/json' }, body: JSON.stringify({ positionMs: 0, durationMs: it.durationMs }) })
}, title)
// The settings open with the "Settings" button and close with it again;
// this opens them if they are not open, with the keyboard.
async function openSettings(p) {
  if (await p.locator('[data-testid=learned]').count()) return
  await focusByText(p, 'Settings')
  await p.keyboard.press('Enter')
  await p.waitForSelector('[data-testid=learned]')
}
// Waits for the film in the player to be the one sent a given way (how it
// is sent is written on the player), nudging it to play as playsBeyond does.
const sentAs = (p, attribute, value, why) => p.waitForFunction(([a, want]) => {
  const v = document.querySelector('video')
  if (!v) return false
  if (v.paused && v.readyState >= 1) {
    v.muted = true
    v.play().catch(() => {})
  }
  return v.dataset[a] === want && !v.paused && v.readyState >= 2
}, [attribute, value], { timeout: 40000, polling: 200 }).catch(async () => {
  const got = await p.evaluate(() => ({ ...document.querySelector('video')?.dataset }))
  assert.fail(`${why}: after 40 seconds the player says ${JSON.stringify(got)}, not ${attribute} ${value} and playing`)
})

assert.ok(existsSync(bin), `program not found at ${bin}; build it first`)
const data = mkdtempSync(join(tmpdir(), 'lm-data-'))
const media = mkdtempSync(join(tmpdir(), 'lm-media-'))
cpSync(join(here, 'fixtures'), media, { recursive: true })
// A small show, made from the same clip, laid out the way shows usually are.
const season = join(media, 'TV', 'Good Show', 'Season 01')
mkdirSync(season, { recursive: true })
for (const n of [1, 2]) copyFileSync(join(here, 'fixtures', 'Test Clip.mp4'), join(season, `Good Show - S01E0${n}.mp4`))
// From M3 the film with Dolby sound is played through the converter, so the
// test needs ffmpeg, and a film long enough to jump around in: 40 seconds,
// with subtitles in a file beside it.
const ffmpeg = process.env.LOCALMEDIA_FFMPEG || 'ffmpeg'
const made = spawnSync(ffmpeg, ['-hide_banner', '-loglevel', 'error', '-y',
  '-f', 'lavfi', '-i', 'testsrc2=size=320x180:rate=24:duration=40', '-f', 'lavfi', '-i', 'sine=frequency=440:duration=40',
  '-c:v', 'libx264', '-preset', 'ultrafast', '-g', '48', '-pix_fmt', 'yuv420p', '-c:a', 'ac3', '-b:a', '96k',
  join(media, 'Dolby.Sound.Test.mkv')], { encoding: 'utf8' })
assert.equal(made.status, 0, `ffmpeg is needed for this test (set LOCALMEDIA_FFMPEG and LOCALMEDIA_FFPROBE, or put both on the PATH): ${made.error || made.stderr}`)
writeFileSync(join(media, 'Dolby.Sound.Test.en.srt'),
  '1\n00:00:00,500 --> 00:00:20,000\nThe subtitle is showing\n\n2\n00:00:20,000 --> 00:00:39,000\nStill in step after a jump\n')
// A longer film with three sound tracks to switch between and subtitles
// stored inside it: 90 seconds, more than a player keeps ready at once.
writeFileSync(join(media, 'three-sounds-subtitles.txt'),
  '1\n00:00:00,500 --> 00:00:45,000\nSaid in the first half\n\n2\n00:00:45,000 --> 00:01:29,000\nSaid in the second half\n')
const made3 = spawnSync(ffmpeg, ['-hide_banner', '-loglevel', 'error', '-y',
  '-f', 'lavfi', '-i', 'testsrc2=size=320x180:rate=24:duration=90', '-f', 'lavfi', '-i', 'sine=frequency=440:duration=90',
  '-f', 'srt', '-i', join(media, 'three-sounds-subtitles.txt'), '-map', '0:v', '-map', '1:a', '-map', '1:a', '-map', '1:a', '-map', '2:s',
  '-c:v', 'libx264', '-preset', 'ultrafast', '-crf', '34', '-g', '48', '-pix_fmt', 'yuv420p', '-c:a', 'ac3', '-b:a', '96k', '-c:s', 'srt',
  '-metadata:s:a', 'language=eng', '-metadata:s:s:0', 'language=eng', '-disposition:a:0', 'default', '-disposition:a:1', '0', '-disposition:a:2', '0',
  join(media, 'Three.Sounds.Test.mkv')], { encoding: 'utf8' })
assert.equal(made3.status, 0, `ffmpeg could not make the film with three sound tracks: ${made3.error || made3.stderr}`)

// A film in a format no browser plays: MPEG-4 Part 2, the format of DivX and
// Xvid films, with MP3 sound. From M4 its picture is converted as it plays.
const madeOld = spawnSync(ffmpeg, ['-hide_banner', '-loglevel', 'error', '-y',
  '-f', 'lavfi', '-i', 'testsrc2=size=320x180:rate=25:duration=30', '-f', 'lavfi', '-i', 'sine=frequency=440:duration=30',
  '-c:v', 'mpeg4', '-q:v', '6', '-g', '100', '-c:a', 'libmp3lame', '-b:a', '96k',
  join(media, 'Old.Format.Test.mkv')], { encoding: 'utf8' })
assert.equal(madeOld.status, 0, `ffmpeg could not make the film in an old format: ${madeOld.error || madeOld.stderr}`)

// "Open in another player" on the PC starts the PC's own video player. No
// player must start on the machine running this test, so the program is
// told to write down what it would have opened instead.
const openedInPlayer = join(data, 'opened-in-player.txt')
const server = spawn(bin, ['--data', data, '--port', String(port), '--no-browser'], {
  stdio: ['ignore', 'pipe', 'pipe'],
  env: { ...process.env, LOCALMEDIA_OPEN_RECORD: openedInPlayer },
})
let serverLog = ''
server.stdout.on('data', (d) => (serverLog += d))
server.stderr.on('data', (d) => (serverLog += d))

const local = `http://localhost:${port}`
const lan = lanAddress()
const remote = lan ? `http://${lan}:${port}` : null
let browser
let failed = false
let whenFailed = () => {}
try {
  for (let i = 0; ; i++) {
    try {
      if ((await fetch(local + '/health')).ok) break
    } catch {}
    if (i > 100) throw new Error('server did not start:\n' + serverLog)
    await new Promise((r) => setTimeout(r, 100))
  }
  console.log(`browser: ${browserName}; PC address ${local}; other-device address ${remote || '(none found, using localhost)'}`)

  // The rules by which the page decides that a film is in trouble need no
  // browser, and have tests of their own. They are run here so that they
  // run wherever this test does.
  await step('the page\'s rules for noticing a film in trouble hold (web/src/stall.test.mjs)', async () => {
    const run = spawnSync(process.execPath, ['--test', join(here, '..', 'web', 'src', 'stall.test.mjs')], { encoding: 'utf8' })
    assert.equal(run.status, 0, `the tests of the rules failed:\n${run.stdout}\n${run.stderr}${run.error || ''}`)
  })

  browser = await playwright[browserName].launch()

  if (remote) {
    await step('before setup, another device is turned away', async () => {
      const res = await fetch(remote + '/')
      assert.equal(res.status, 403)
      assert.match(await res.text(), /Setup is not finished/)
      assert.equal((await fetch(remote + '/api/movies')).status, 403)
    })
  }

  const pc = await browser.newContext()
  const setupPage = await pc.newPage()
  await step('setup on the PC, keyboard only', async () => {
    await setupPage.goto(local)
    await setupPage.waitForSelector('#name')
    await pressUntilFocused(setupPage, '#name')
    await setupPage.keyboard.type('owner')
    await pressUntilFocused(setupPage, '#password')
    await setupPage.keyboard.type('a good password')
    await pressUntilFocused(setupPage, '#folder')
    await setupPage.keyboard.press(process.platform === 'darwin' ? 'Meta+A' : 'Control+A')
    await setupPage.keyboard.type(media)
    await setupPage.keyboard.press('Enter')
    await setupPage.waitForSelector('text=Films (4)')
  })

  await step('the PC shows the address and a QR code for other devices', async () => {
    await focusByText(setupPage, 'Open on another device')
    await setupPage.keyboard.press('Enter')
    await setupPage.waitForSelector('[data-testid=connect]')
    const res = await setupPage.request.get(local + '/api/connect/qr.png?i=0')
    if (remote) {
      assert.equal(res.status(), 200)
      assert.equal(res.headers()['content-type'], 'image/png')
      assert.ok((await setupPage.textContent('[data-testid=connect]')).includes(remote))
    }
  })

  // Everything from here runs the way a phone would reach it: a fresh
  // browser with no sign-in, over plain HTTP at a network address.
  const base = remote || local
  const phone = await browser.newContext({ viewport: { width: 390, height: 844 } })
  const page = await phone.newPage()
  const consoleErrors = []
  page.on('console', (m) => m.type() === 'error' && consoleErrors.push(m.text()))
  // Every report of trouble the page sends to the PC is noted, with the
  // step it was sent in. Only some steps provoke one. On a machine that is
  // too slow to play the test films a film may stall by itself, and what
  // the PC then learns changes how later films are sent; when a step
  // fails, this record says whether that happened.
  const troubleReports = []
  page.on('request', (r) => {
    if (r.method() === 'POST' && /\/api\/hls\/[0-9a-f]+\/trouble$/.test(r.url())) troubleReports.push(`after "${steps[steps.length - 1]}": ${r.postData()}`)
  })
  whenFailed = () => console.error(`\n--- trouble the page reported to the PC in this run (${troubleReports.length}) ---\n${troubleReports.join('\n') || '(none)'}`)

  await step('another device must sign in; a wrong password is refused', async () => {
    assert.equal((await page.request.get(base + '/api/movies')).status(), 401)
    await page.goto(base)
    await page.waitForSelector('text=Sign in to Local Media')
    await pressUntilFocused(page, '#name')
    await page.keyboard.type('owner')
    await pressUntilFocused(page, '#password')
    await page.keyboard.type('not the password')
    await page.keyboard.press('Enter')
    await page.waitForSelector('text=Wrong user name or password')
    await pressUntilFocused(page, '#password')
    await page.keyboard.press(process.platform === 'darwin' ? 'Meta+A' : 'Control+A')
    await page.keyboard.type('a good password')
    await page.keyboard.press('Enter')
    await page.waitForSelector('text=Films (4)')
    // The line at the foot of the page describes this version, not the
    // first one, which played only what a browser plays by itself.
    const foot = await page.textContent('main')
    assert.match(foot, /repackaged while you watch/)
    assert.doesNotMatch(foot, /plays only files your browser can play by itself/)
  })

  await step('the page tells the PC whether this device plays 10-bit H.264, and HEVC in which labelling', async () => {
    const r = await page.evaluate(async () => {
      const can = (...types) => types.some((t) => document.createElement('video').canPlayType(t) !== '')
      return {
        told: (await (await fetch('/api/diagnostics')).json()).device.video,
        h264in10bit: can('video/mp4; codecs="avc1.6E0033"'),
        hevc: can('video/mp4; codecs="hvc1.1.6.L120.90"', 'video/mp4; codecs="hev1.1.6.L120.90"'),
        hev1: can('video/mp4; codecs="hev1.1.6.L120.90"', 'video/mp4; codecs="hev1.2.4.L120.90"'),
        apple: /Apple/.test(navigator.vendor || ''),
      }
    })
    console.log(`      (told the PC: ${Object.keys(r.told).filter((k) => r.told[k]).join(', ')})`)
    // The PC looks these up by name when it decides how to play a file. A
    // name the page never sends counts as "cannot play".
    assert.equal(r.told['hevc-hev1'], r.hev1, 'HEVC labelled hev1: what the page told the PC against what the browser says')
    assert.equal(r.told.hevc, r.hevc, 'HEVC is still reported when either labelling plays')
    // Apple's browsers say they play 10-bit H.264 but are not known to show
    // its picture, so from them the page must not pass that answer on.
    if (r.apple) {
      assert.equal(r.told['h264-10'], false, `10-bit H.264 in an Apple browser (which itself says: ${r.h264in10bit}) must be reported as not playing`)
      return
    }
    assert.equal(r.told['h264-10'], r.h264in10bit, '10-bit H.264: what the page told the PC against what the browser says')
    if (browserName === 'chromium') assert.equal(r.told['h264-10'], true, 'Chromium plays 10-bit H.264, and must not be told it cannot')
    // The same rule where this test runs in another engine: a device that
    // differs from this one only in calling itself Apple's.
    const apple = await browser.newContext()
    await apple.addInitScript(() => Object.defineProperty(Navigator.prototype, 'vendor', { configurable: true, get: () => 'Apple Computer, Inc.' }))
    const iphone = await apple.newPage()
    await iphone.goto(base)
    await iphone.fill('#name', 'owner')
    await iphone.fill('#password', 'a good password')
    const reported = iphone.waitForResponse((res) => /\/api\/capabilities$/.test(res.url()))
    await iphone.press('#password', 'Enter')
    assert.equal((await reported).status(), 200, 'the device calling itself Apple\'s reported what it plays')
    const a = await iphone.evaluate(async () => ({
      told: (await (await fetch('/api/diagnostics')).json()).device.video,
      itself: document.createElement('video').canPlayType('video/mp4; codecs="avc1.6E0033"') !== '',
    }))
    await apple.close()
    assert.equal(a.told['h264-10'], false, `10-bit H.264 on a device calling itself Apple's (its browser itself says: ${a.itself}) must be reported as not playing`)
    assert.equal(a.told.h264, true, 'ordinary H.264 is still reported from that device')
  })

  await step('play a file, keyboard only', async () => {
    await focusByText(page, 'Test Clip')
    await page.keyboard.press('Enter')
    await waitPlaying(page, 1)
    const w = await page.evaluate(() => document.querySelector('video').videoWidth)
    assert.equal(w, 320, 'the picture is decoded')
    assert.equal(await page.locator('[data-testid=play-problem]').count(), 0, `nothing is wrong, yet the page says: ${await page.locator('[data-testid=play-problem]').allTextContents()}`)
  })

  await step('seek, then leave', async () => {
    await page.evaluate(() => (document.querySelector('video').currentTime = 12))
    await page.waitForFunction(() => document.querySelector('video').currentTime > 12.5, null, { timeout: 20000 })
    await page.evaluate(() => document.querySelector('video').pause())
    await focusByText(page, '← Back')
    await page.keyboard.press('Enter')
    await page.waitForSelector('text=Continue watching')
    await page.waitForSelector('text=Stopped at 0:1')
  })

  await step('resume where it stopped', async () => {
    await focusByText(page, 'Test Clip')
    await page.keyboard.press('Enter')
    await waitPlaying(page, 11)
    const t = await page.evaluate(() => document.querySelector('video').currentTime)
    assert.ok(t >= 11 && t < 19, `resumed at ${t}`)
    await page.goBack()
    await page.waitForSelector('text=Films (4)')
  })

  // How many films the PC holds open for conversion.
  const converters = () => page.evaluate(async () => (await (await fetch('/api/diagnostics')).json()).open)

  await step('a film this browser cannot play as it is plays anyway: repackaged as it plays', async () => {
    await focusByText(page, 'Dolby Sound Test')
    await page.keyboard.press('Enter')
    await waitPlaying(page, 2)
    const v = await page.evaluate(() => {
      const el = document.querySelector('video')
      return { method: el.dataset.method, width: el.videoWidth, duration: el.duration, sound: el.webkitAudioDecodedByteCount }
    })
    // Chromium cannot play Dolby sound, so it is converted. Safari's engine
    // can, so there the file is only repackaged.
    assert.ok(v.method === 'audio' || v.method === 'remux', `played by method ${v.method}`)
    if (browserName === 'chromium') {
      assert.equal(v.method, 'audio')
      assert.ok(v.sound > 0, 'the converted sound is being decoded')
    }
    assert.equal(v.width, 320, 'the picture is decoded')
    assert.ok(Math.abs(v.duration - 40) < 0.5, `the timeline is the whole film: ${v.duration}`)
    const why = await page.textContent('[data-testid=how-text]')
    assert.match(why, /Repackaging the file/)
    assert.equal(await converters(), 1, 'the PC holds one film open for conversion')
    // Safari's engine learns the size of a repackaged film's picture a
    // moment after the film opens, which the page once took for "no picture".
    assert.equal(await page.locator('[data-testid=play-problem]').count(), 0, `nothing is wrong, yet the page says: ${await page.locator('[data-testid=play-problem]').allTextContents()}`)
  })

  await step('subtitles show, and are still in step after a jump far ahead', async () => {
    const choices = []
    const noteChoice = (r) => /\/choice$/.test(r.url()) && choices.push(r.postDataJSON())
    page.on('request', noteChoice)
    await pressUntilFocused(page, '[data-testid=pick-subtitles]')
    await page.keyboard.press('ArrowDown')
    if ((await page.inputValue('[data-testid=pick-subtitles]')) === '') await page.selectOption('[data-testid=pick-subtitles]', { index: 1 })
    // What is remembered is the track itself, not only its language: a
    // film can have two sound tracks in one language, and subtitles with
    // no language named at all.
    for (let i = 0; choices.length === 0; i++) {
      assert.ok(i < 50, 'the page did not tell the PC which subtitles were picked')
      await new Promise((r) => setTimeout(r, 100))
    }
    page.off('request', noteChoice)
    const picked = choices[choices.length - 1]
    assert.equal(picked.subKey, await page.inputValue('[data-testid=pick-subtitles]'), `the subtitles picked are named in what the PC is told to remember: ${JSON.stringify(picked)}`)
    assert.equal(picked.audioIndex, 0, `the sound track playing is named in what the PC is told to remember: ${JSON.stringify(picked)}`)
    const cue = () => page.evaluate(() => {
      const t = document.querySelector('video').textTracks[0]
      return t && t.mode === 'showing' && t.activeCues && t.activeCues.length ? t.activeCues[0].text : ''
    })
    await page.waitForFunction(() => {
      const t = document.querySelector('video').textTracks[0]
      return t && t.activeCues && t.activeCues.length > 0
    }, null, { timeout: 15000 })
    assert.equal(await cue(), 'The subtitle is showing')
    const before = Date.now()
    await page.evaluate(() => (document.querySelector('video').currentTime = 31))
    await page.waitForFunction(() => document.querySelector('video').currentTime > 31.5, null, { timeout: 20000 })
    console.log(`      (the jump took ${Date.now() - before} ms including half a second of playing)`)
    assert.equal(await cue(), 'Still in step after a jump')
    await page.evaluate(() => document.querySelector('video').pause())
  })

  await step('leaving the player stops the converter', async () => {
    await focusByText(page, '← Back')
    await page.keyboard.press('Enter')
    await page.waitForSelector('text=Continue watching')
    for (let i = 0; (await converters()) > 0; i++) {
      assert.ok(i < 50, 'the film is still held open for conversion 5 seconds after leaving')
      await new Promise((r) => setTimeout(r, 100))
    }
  })

  await step('a film in a format no browser plays has its picture converted as it plays, and survives a jump', async () => {
    await focusByText(page, 'Old Format Test')
    await page.keyboard.press('Enter')
    await waitPlaying(page, 2)
    const v = await page.evaluate(() => {
      const el = document.querySelector('video')
      return { method: el.dataset.method, rung: el.dataset.rung, width: el.videoWidth, height: el.videoHeight, duration: el.duration, sound: el.webkitAudioDecodedByteCount }
    })
    assert.equal(v.method, 'convert', `played by method ${v.method}`)
    assert.equal(v.rung, '1', 'an ordinary small film is converted at the best setting')
    assert.equal(`${v.width}x${v.height}`, '320x180', 'the converted picture is decoded, at the film\'s own size')
    assert.ok(Math.abs(v.duration - 30) < 0.5, `the timeline is the whole film: ${v.duration}`)
    if (browserName === 'chromium') assert.ok(v.sound > 0, 'the converted sound is being decoded')
    assert.match(await page.textContent('[data-testid=how-text]'), /Converting the picture/)
    assert.match(await page.textContent('[data-testid=how-playing]'), /MPEG-4/, 'the panel says which format stood in the way')
    assert.equal(await page.locator('[data-testid=play-problem]').count(), 0, `nothing is wrong, yet the page says: ${await page.locator('[data-testid=play-problem]').allTextContents()}`)
    const before = Date.now()
    await page.evaluate(() => (document.querySelector('video').currentTime = 20))
    await page.waitForFunction(() => document.querySelector('video').currentTime > 20.5, null, { timeout: 20000 })
      .catch(() => assert.fail('the converted film did not carry on after a jump'))
    console.log(`      (the jump took ${Date.now() - before} ms including half a second of playing)`)
    await focusByText(page, '← Back')
    await page.keyboard.press('Enter')
    await page.waitForSelector('text=Continue watching')
    for (let i = 0; (await converters()) > 0; i++) {
      assert.ok(i < 50, 'the film is still held open for conversion 5 seconds after leaving')
      await new Promise((r) => setTimeout(r, 100))
    }
  })

  // Leaves the player with the keyboard and waits for the PC to have
  // closed the film.
  const leavePlayer = async () => {
    await focusByText(page, '← Back')
    await page.keyboard.press('Enter')
    await page.waitForSelector('text=Continue watching')
    for (let i = 0; (await converters()) > 0; i++) {
      assert.ok(i < 50, 'the film is still held open for conversion 5 seconds after leaving')
      await new Promise((r) => setTimeout(r, 100))
    }
  }
  // The app's own chunk player fetches the film's parts through the page,
  // where this test can hold them back. Safari's engine plays chunks with
  // a player of its own, whose requests a test does not get to see.
  const ownChunkPlayer = browserName !== 'webkit'
  const tokenOf = (url) => (/\/hls\/([0-9a-f]+)\//.exec(url) || [])[1]

  await step('a film that is slow to start: the viewer is told after three seconds and offered ways out after twelve, keyboard only', async () => {
    await fromTheStart(page, 'Old Format Test')
    // The film's start is held back until the test lets go. With the app's
    // own player, the first part of the first film asked for is held: the
    // PC has answered by then, so the page knows a lighter picture can be
    // had. With the browser's own player, the PC's answer itself is held.
    let release
    const letGo = new Promise((r) => (release = r))
    let firstFilm
    const held = ownChunkPlayer ? /\/hls\/[0-9a-f]+\/init\.mp4$/ : /\/api\/items\/[0-9a-f]+\/play$/
    const hold = async (route) => {
      const film = tokenOf(route.request().url()) || 'the answer'
      firstFilm = firstFilm || film
      if (film === firstFilm) await letGo
      await route.continue().catch(() => {}) // the player may have dropped the request meanwhile
    }
    await page.route(held, hold)
    try {
      const since = Date.now()
      await focusByText(page, 'Old Format Test')
      await page.keyboard.press('Enter')
      await page.waitForSelector('[data-testid=wait-words] p', { timeout: 30000 }).catch(() => assert.fail('nothing was said about a film that had not started after many seconds'))
      assert.equal(await page.textContent('[data-testid=wait-words]'), 'Still getting the film ready…')
      assert.ok(Date.now() - since >= 2500, `the words came after ${Date.now() - since} ms: a film is given three seconds before anything is said`)
      await page.waitForSelector('[data-testid=wait-card]', { timeout: 40000 }).catch(() => assert.fail('no ways out were offered for a film that had not started after twelve seconds and more'))
      assert.ok(Date.now() - since >= 11500, `the ways out came after ${Date.now() - since} ms: a film is given twelve seconds first`)
      // Every way out can be reached with the keyboard, and is in the card.
      const inCard = () => page.evaluate(() => !!document.activeElement.closest('[data-testid=wait-card]'))
      await focusByText(page, 'Go back')
      assert.ok(await inCard(), '"Go back" is one of the ways out')
      await focusByText(page, 'Open in another player')
      assert.ok(await inCard(), '"Open in another player" is one of the ways out')
      // Waiting for a film to start is not reported to the PC as trouble:
      // nothing has been learned from it.
      assert.deepEqual(await learned(page), [], 'a slow start alone teaches the PC nothing about this device')
      const cardGone = () => page.waitForSelector('[data-testid=wait-card]', { state: 'detached', timeout: 10000 }).catch(() => assert.fail('the ways out stayed on the page after the film had started'))
      if (!ownChunkPlayer) {
        console.log('      (in this browser the PC\'s answer was held back, so no lighter picture was on offer yet)')
        release()
        await waitPlaying(page, 0.5)
        await cardGone()
      } else {
        // A lighter picture is on offer, because the PC said one can be had.
        await focusByText(page, 'Play with a lighter picture')
        assert.ok(await inCard(), '"Play with a lighter picture" is one of the ways out')
        await page.keyboard.press('Enter')
        await sentAs(page, 'rung', '2', 'asked for a lighter picture')
        await cardGone()
        assert.equal(await page.textContent('[data-testid=wait-words]'), '', 'nothing is said about waiting once the film plays')
        assert.match(await page.textContent('[data-testid=step-said]'), /^The film now plays with a lighter picture/)
        // The button that was pressed is gone with the card; the keyboard's
        // place is on what the page says happened, and Undo is the next stop.
        assert.ok(await hasFocus(page, '[data-testid=step-notice]'), 'after the press the keyboard is on the notice about what happened')
        await page.keyboard.press(TAB)
        assert.ok(await hasFocus(page, '[data-testid=step-undo]'), 'the keyboard goes on from the notice to Undo')
        await page.keyboard.press('Enter')
        await sentAs(page, 'rung', '1', 'after Undo')
        assert.match(await page.textContent('[data-testid=step-said]'), /^Undone: the film plays the way it did before\./)
        assert.equal(await page.locator('[data-testid=step-undo]').count(), 0, 'there is nothing left to undo')
        assert.ok(await hasFocus(page, '[data-testid=step-notice]'), 'after Undo the keyboard is on the notice')
      }
    } finally {
      release()
      await page.unroute(held, hold)
    }
    await page.evaluate(() => document.querySelector('video').pause())
    await leavePlayer()
  })

  await step('a film that runs dry while it plays is started again lighter; what was learned is listed, the next play starts lower, and Forget puts it back', async () => {
    assert.deepEqual(await learned(page), [], 'before this step nothing may have been learned about this device (a film stalled by itself earlier in this run: is the machine too slow to play the test films?)')
    await fromTheStart(page, 'Old Format Test')
    // The film plays its first nine seconds and then runs dry. With the
    // app's own player the film's parts are really held back on their way
    // to it, all but the first three. Safari's engine fetches them out of
    // this test's reach, so there the player is made to look as a player
    // that has run dry looks: its clock stands still and it holds nothing
    // to show next.
    let holding = true
    const parts = new Map() // how many parts of each film have been asked for
    const waiting = []
    const chunk = /\/hls\/[0-9a-f]+\/seg-\d+\.m4s$/
    const hold = async (route) => {
      const film = tokenOf(route.request().url())
      const n = (parts.get(film) || 0) + 1
      parts.set(film, n)
      if (holding && n > 3) await new Promise((r) => waiting.push(r))
      await route.continue().catch(() => {}) // the player may have dropped the request meanwhile
    }
    const letGo = async () => {
      holding = false
      waiting.splice(0).forEach((r) => r())
      if (ownChunkPlayer) await page.unroute(chunk, hold)
      else await page.evaluate(() => window.playerMovesAgain && window.playerMovesAgain())
    }
    if (ownChunkPlayer) await page.route(chunk, hold)
    // (Listened for from before the film opens, so that it cannot be missed.)
    const reported = page.waitForRequest((r) => r.method() === 'POST' && /\/api\/hls\/[0-9a-f]+\/trouble$/.test(r.url()), { timeout: 90000 })
    reported.catch(() => {}) // looked at below
    let said
    let stoodAt
    try {
      await focusByText(page, 'Old Format Test')
      await page.keyboard.press('Enter')
      await waitPlaying(page, 1)
      assert.equal(await page.evaluate(() => document.querySelector('video').dataset.rung), '1', 'the film starts at the best setting')
      if (!ownChunkPlayer) {
        await page.evaluate(() => {
          const proto = HTMLMediaElement.prototype
          const time = Object.getOwnPropertyDescriptor(proto, 'currentTime')
          const ready = Object.getOwnPropertyDescriptor(proto, 'readyState')
          const at = time.get.call(document.querySelector('video'))
          Object.defineProperty(proto, 'currentTime', { configurable: true, get: () => at, set: time.set })
          Object.defineProperty(proto, 'readyState', { configurable: true, get: () => 2 })
          window.playerMovesAgain = () => {
            Object.defineProperty(proto, 'currentTime', time)
            Object.defineProperty(proto, 'readyState', ready)
          }
        })
        console.log('      (in this browser the film\'s parts cannot be held back; its player was made to look run dry instead)')
      }
      // The viewer is told while the film stands still.
      await page.waitForSelector('[data-testid=wait-words] p', { timeout: 60000 }).catch(() => assert.fail('nothing was said while the film stood still'))
      stoodAt = await page.evaluate(() => document.querySelector('video').currentTime)
      // The page tells the PC, once the film has stood still for eight
      // seconds beyond the ten in which a wait after a start is normal.
      said = (await reported.catch(() => assert.fail('a film that stood still for a long time was not reported to the PC'))).postDataJSON()
      assert.equal(said.evidence, 'starved', `the page reported: ${JSON.stringify(said)}`)
      if (ownChunkPlayer) assert.ok(waiting.length > 0, 'parts of the film were being held back when the page reported trouble')
      // The PC kept up with converting; it was the way to this device that
      // was slow. So the film is started again where it was, at a setting
      // that takes fewer bits, and the viewer is told why. (Its first parts
      // come through like the first film's did.)
      await sentAs(page, 'rung', '3', `after the stall (the page reported ${JSON.stringify(said)})`)
    } finally {
      await letGo()
    }
    assert.equal(await page.textContent('[data-testid=step-said]'), 'The film was reaching this device too slowly, so the PC now makes the picture at a lighter setting.')
    const at = await page.evaluate(() => document.querySelector('video').currentTime)
    assert.ok(at > stoodAt - 1 && at < stoodAt + 15, `the film carries on where it stood still (${stoodAt} s), not at ${at} s`)
    if (ownChunkPlayer) assert.ok(stoodAt > 5 && stoodAt < 12.5, `with all but its first three parts held back the film stands still about 9 seconds in, not at ${stoodAt} s`)
    assert.equal(await page.locator('[data-testid=play-problem]').count(), 0, `nothing else is wrong, yet the page says: ${await page.locator('[data-testid=play-problem]').allTextContents()}`)
    // Undo can be reached with the keyboard. (It is pressed in a later
    // step; here the lesson is wanted.)
    await focusByText(page, 'Undo')
    assert.ok(await hasFocus(page, '[data-testid=step-undo]'))
    await page.evaluate(() => document.querySelector('video').pause())
    await leavePlayer()

    // What was learned is listed in the settings, in a sentence.
    await openSettings(page)
    await page.waitForSelector('[data-testid=learned-list] li')
    const listed = await page.locator('[data-testid=learned-list] li').allTextContents()
    assert.equal(listed.length, 1, `one thing was learned: ${JSON.stringify(listed)}`)
    assert.match(listed[0], /^Films were reaching this device too slowly, so for a day films that need more than 5 megabits a second are converted to a lighter picture for it\. Remembered until .+\.Forget$/)
    // The next play starts lower by itself, with nothing said: nothing
    // went wrong this time.
    await fromTheStart(page, 'Old Format Test')
    await focusByText(page, 'Old Format Test')
    await page.keyboard.press('Enter')
    await waitPlaying(page, 1)
    assert.equal(await page.evaluate(() => document.querySelector('video').dataset.rung), '3', 'the next play starts at the lighter setting')
    assert.equal(await page.textContent('[data-testid=step-notice]'), '', 'nothing is said when a film simply starts the way the PC has learned to send it')
    await page.evaluate(() => document.querySelector('video').pause())
    await leavePlayer()

    // Forget, with the keyboard, puts it back.
    await openSettings(page)
    await focusByText(page, 'Forget')
    await page.keyboard.press('Enter')
    await page.waitForSelector('[data-testid=learned-none]').catch(() => assert.fail('the lesson was still listed after Forget'))
    assert.match(await page.textContent('[data-testid=learned-said]'), /^Forgotten\./)
    // The button that was pressed is gone; the keyboard's place is on the
    // line that says what happened.
    assert.ok(await hasFocus(page, '[data-testid=learned-said]'), 'after Forget the keyboard is on the line that says it was forgotten')
    assert.deepEqual(await learned(page), [])
    await fromTheStart(page, 'Old Format Test')
    await focusByText(page, 'Old Format Test')
    await page.keyboard.press('Enter')
    await waitPlaying(page, 1)
    assert.equal(await page.evaluate(() => document.querySelector('video').dataset.rung), '1', 'after Forget the film starts at the best setting again')
    await page.evaluate(() => document.querySelector('video').pause())
    await leavePlayer()
  })

  await step('the page asks the PC about a film it has open: one the PC has closed is started again quietly, and a conversion the PC says is too slow is stepped down before it stalls', async () => {
    assert.deepEqual(await learned(page), [], 'before this step nothing may have been learned about this device')
    await fromTheStart(page, 'Old Format Test')
    // The PC closes a film behind the page's back when it is restarted. It
    // is not restarted here; the film is closed on it the way the page
    // itself closes one, without the page hearing of it.
    const tokens = [] // the name of each play the PC opened, newest last
    const notePlay = async (res) => {
      if (res.request().method() === 'POST' && /\/api\/items\/[0-9a-f]+\/play$/.test(res.url())) tokens.push((await res.json().catch(() => ({}))).play)
    }
    page.on('response', notePlay)
    const closedOnThePC = async () => {
      const closed = await page.evaluate(async (t) => {
        await fetch(`/api/hls/${t}/stop`, { method: 'POST', headers: { 'X-Requested-With': 'localmedia', 'Content-Type': 'application/json' }, body: '{}' })
        return (await (await fetch(`/api/hls/${t}/state`)).json()).alive === false
      }, tokens[tokens.length - 1])
      assert.ok(closed, 'the PC says the film is no longer open')
    }
    // Nor is this PC too slow for the small test film. For that part, what
    // it says about the film is changed on its way to the page, one answer.
    // What the page then does, and the PC with it, is real.
    let say = null
    let asks = 0
    const state = /\/api\/hls\/[0-9a-f]+\/state$/
    const changed = async (route) => {
      asks++
      const res = await route.fetch()
      const told = { ...(await res.json()), ...(say || {}) }
      say = null
      await route.fulfill({ response: res, json: told })
    }
    const opened = () => {
      const r = page.waitForRequest((q) => q.method() === 'POST' && /\/api\/items\/[0-9a-f]+\/play$/.test(q.url()), { timeout: 30000 })
      r.catch(() => {})
      return r
    }
    // The film in the player is another one than src, and has loaded.
    const replaced = (src, why) => page.waitForFunction((was) => {
      const v = document.querySelector('video')
      return !!v && !!v.currentSrc && v.currentSrc !== was && v.readyState >= 1
    }, src, { timeout: 30000 }).catch(() => assert.fail(why))
    const player = () => page.evaluate(() => {
      const v = document.querySelector('video')
      return { src: v.currentSrc, at: v.currentTime, paused: v.paused, rung: v.dataset.rung }
    })
    await page.route(state, changed)
    try {
      await focusByText(page, 'Old Format Test')
      await page.keyboard.press('Enter')
      await waitPlaying(page, 1)
      // The viewer comes back to the page (a phone is unlocked, a tab is
      // brought to the front): the page asks whether the film is still
      // open on the PC. Told that it is not, it starts the film again where
      // it is, and says nothing: there is nothing for the viewer to do.
      // (If the player is still fetching the film when the PC closes it,
      // the next part is refused and the page finds out by that, sooner.
      // So the page's request is listened for from before the closing.)
      let before = await player()
      let again = opened()
      await closedOnThePC()
      await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
      let asked = (await again.catch(() => assert.fail('told that the PC had closed the film, the page did not ask for it again'))).postDataJSON()
      assert.match(asked.from || '', /^[0-9a-f]{32}$/, `the page names the play that is replaced: ${JSON.stringify(asked)}`)
      await replaced(before.src, 'the film the PC had closed was not handed to the player again')
      await playsBeyond(page, before.at + 0.5, 'a film started again after the PC had closed it')
      let now = await player()
      assert.ok(now.at < before.at + 15, `the film was at ${before.at} s and carries on at ${now.at} s`)
      assert.equal(await page.textContent('[data-testid=step-notice]'), '', 'nothing is said about it')
      assert.equal(await page.locator('[data-testid=play-problem]').count(), 0, `nothing is wrong, yet the page says: ${await page.locator('[data-testid=play-problem]').allTextContents()}`)
      // A film the viewer had paused (a phone pauses a film when it is
      // locked) is started again the same way, and stays paused.
      await page.evaluate(() => document.querySelector('video').pause())
      before = await player()
      again = opened()
      await closedOnThePC()
      await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
      await again.catch(() => assert.fail('told that the PC had closed a paused film, the page did not ask for it again'))
      await replaced(before.src, 'the paused film the PC had closed was not handed to the player again')
      // (With the app's own player the look is taken once the player holds
      // enough of the film to have started it by itself, had it meant to.)
      await page.waitForFunction(([was, enough]) => {
        const v = document.querySelector('video')
        return v.readyState >= enough && Math.abs(v.currentTime - was) < 3.5
      }, [before.at, ownChunkPlayer ? 3 : 1], { timeout: 30000 }).catch(async () => assert.fail(`the paused film was at ${before.at} s and is now at ${(await player()).at} s`))
      now = await player()
      assert.ok(now.paused, 'a film that was paused is still paused after being started again')

      // Twelve seconds into a film whose picture is being converted, the
      // page asks how the conversion is going. Told that the PC converts
      // slower than the film plays, it reports that as trouble at once,
      // without waiting for the film to stall.
      const asksBefore = asks
      say = { advice: 'lower', pace: 0.5, chunks: 3 }
      const reported = page.waitForRequest((q) => q.method() === 'POST' && /\/api\/hls\/[0-9a-f]+\/trouble$/.test(q.url()), { timeout: 90000 })
      reported.catch(() => {})
      await page.evaluate(() => document.querySelector('video').play().catch(() => {}))
      const said = (await reported.catch(() => assert.fail(`the page did not report a conversion the PC called too slow (it asked the PC ${asks - asksBefore} times)`))).postDataJSON()
      assert.equal(said.evidence, 'starved', `the page reported: ${JSON.stringify(said)}`)
      assert.equal(said.detail, 'the PC said it converts 0.5 seconds of film a second')
      const when = (await player()).at
      assert.ok(when - now.at > 11 && when - now.at < 25, `the PC was asked after ${(when - now.at).toFixed(1)} s of playing; twelve is the rule`)
      // What comes of the report is for the PC to say. (This PC kept up in
      // truth, so it takes the way to the device for the slow part and
      // sends a lighter picture.) The page does as it is told, says so, and
      // offers Undo, which is taken here so that nothing learned is left.
      await page.waitForSelector('[data-testid=step-undo]', { timeout: 30000 }).catch(() => assert.fail('after the report nothing was said, or nothing could be undone'))
      await focusByText(page, 'Undo')
      await page.keyboard.press('Enter')
      await sentAs(page, 'rung', '1', 'after Undo')
      assert.deepEqual(await learned(page), [], 'Undo has the PC forget what it learned')
    } finally {
      page.off('response', notePlay)
      await page.unroute(state, changed)
    }
    await page.evaluate(() => document.querySelector('video').pause())
    await leavePlayer()
  })

  // "Show subtitles" in the settings, set with the keyboard.
  const showSubtitles = async (when) => {
    await focusByText(page, 'Settings')
    await page.keyboard.press('Enter')
    await page.waitForSelector('[data-testid=pref-when]')
    await pressUntilFocused(page, '[data-testid=pref-when]')
    await page.keyboard.press(when === 'always' ? 'ArrowDown' : 'ArrowUp')
    if ((await page.inputValue('[data-testid=pref-when]')) !== when) await page.selectOption('[data-testid=pref-when]', when)
    await page.waitForFunction(async (w) => (await (await fetch('/api/prefs')).json()).subs === w, when, { timeout: 10000 })
  }
  // Waits until the subtitles that are switched on show the given words.
  const subtitleShows = (words) => page.waitForFunction((w) => {
    const t = Array.from(document.querySelector('video').textTracks).find((x) => x.mode === 'showing')
    return !!t && !!t.activeCues && t.activeCues.length > 0 && t.activeCues[0].text.trim() === w
  }, words, { timeout: 15000 }).catch(() => assert.fail(`the subtitles did not come to show "${words}"`))

  await step('a film starts without waiting for its subtitles, and they join it when they arrive', async () => {
    await showSubtitles('always')
    // Subtitles stored inside a film take as long to arrive as the PC takes
    // to read the film through: minutes, for a long one. Here they are held
    // back until the film has been seen playing.
    let wanted = false
    let release
    const held = new Promise((r) => (release = r))
    await page.route('**/subtitles/**', async (route) => {
      wanted = true
      await held
      await route.continue()
    })
    await focusByText(page, 'Three Sounds Test')
    await page.keyboard.press('Enter')
    await waitPlaying(page, 1).catch(() => assert.fail('the film did not start while its subtitles were still on their way'))
    assert.notEqual(await page.inputValue('[data-testid=pick-subtitles]'), '', 'subtitles were chosen when the film opened')
    for (let i = 0; !wanted; i++) {
      assert.ok(i < 50, 'the chosen subtitles were not asked for once the film was playing')
      await new Promise((r) => setTimeout(r, 100))
    }
    release()
    await subtitleShows('Said in the first half')
    await page.unroute('**/subtitles/**')
    // Subtitles can also be switched in the browser's own menu, which the
    // page does not hear about. What was set there must be left alone when
    // the film stops and plays again, as it does after every jump: here
    // they are switched off that way, and must still be off afterwards.
    await page.evaluate(() => {
      const v = document.querySelector('video')
      window.switchedByHand = Array.from(v.textTracks).find((x) => x.mode === 'showing')
      window.switchedByHand.mode = 'disabled'
      v.currentTime = 50
    })
    await playsBeyond(page, 50.5, 'a jump to 50 seconds')
    const after = await page.evaluate(() => {
      const mode = window.switchedByHand.mode
      window.switchedByHand.mode = 'showing'
      return mode
    })
    assert.equal(after, 'disabled', 'subtitles switched off in the browser\'s own menu, after a jump')
  })

  await step('changing the sound twice in a row keeps the place in the film, and the subtitles', async () => {
    const subtitles = await page.inputValue('[data-testid=pick-subtitles]')
    // In Safari's engine this step once took 15 seconds where 4 are usual,
    // and nothing said which of its waits that was, or why. Until that is
    // known a record is kept and printed, whether the step passes or fails:
    // how long each wait took, what the player went through and what it
    // fetched.
    const waits = []
    let since = Date.now()
    const waited = (what) => {
      waits.push(`${what} ${Date.now() - since} ms`)
      since = Date.now()
    }
    const recorded = await record(page)
    // Picks twice and checks the film plays on from where it was, with the
    // sound picked last.
    const twice = async (how, last, pick) => {
      const place = await page.evaluate(() => document.querySelector('video').currentTime)
      since = Date.now()
      await pick()
      await playsBeyond(page, place + 0.5, `after two changes of sound, ${how}`)
      waited(`to play on with ${how}`)
      const v = await page.evaluate(() => {
        const el = document.querySelector('video')
        return { at: el.currentTime, sound: document.querySelector('[data-testid=pick-sound]').value, method: el.dataset.method }
      })
      assert.ok(v.at < place + 15, `${how}: the film was at ${place} s and is now at ${v.at} s`)
      assert.equal(v.sound, last, `${how}: the list shows the sound track picked last`)
      // The PC opened the film once for each pick. The one that was not used
      // must be closed again, leaving the one that is playing.
      for (let i = 0; (await converters()) !== 1; i++) {
        assert.ok(i < 50, `${how}: ${await converters()} films are held open for conversion 5 seconds after changing the sound; one is playing`)
        await new Promise((r) => setTimeout(r, 100))
      }
      waited('for the film not used to be closed')
      console.log(`      (${how}: was at ${place.toFixed(2)} s, plays on at ${v.at.toFixed(2)} s by method ${v.method})`)
    }
    try {
      await page.evaluate(() => (document.querySelector('video').currentTime = 60))
      await playsBeyond(page, 60.5, 'a jump to one minute')
      waited('for the jump')
      // The PC answers each pick by opening the film afresh. Its answer to
      // the first pick can come after the second pick or before it, and the
      // page has a different job in each case, so both are tried. Left to
      // chance, the same one came up nearly every time.
      //
      // First the second pick before the PC can have answered: the first
      // answer must then be left unused, and the film it opened closed. Not
      // with the keyboard for once: until the PC answers, the list goes
      // back to the track that is playing, so two key presses cannot be
      // aimed at the third track, nor be sure to beat the answer.
      await twice('the second pick ahead of the first answer', '2', () => page.evaluate(() => {
        const list = document.querySelector('[data-testid=pick-sound]')
        for (const track of ['1', '2']) {
          list.value = track
          list.dispatchEvent(new Event('change', { bubbles: true }))
        }
      }))
      // Then the second pick as soon as the first answer is in use (the list
      // then shows the first pick): the player is starting afresh at that
      // moment and its clock reads nothing.
      await twice('the second pick just after the first answer', '0', async () => {
        const answer = page.waitForResponse((res) => /\/api\/items\/[^/]+\/play$/.test(res.url()))
        await page.selectOption('[data-testid=pick-sound]', '1')
        await answer
        await page.waitForFunction(() => document.querySelector('[data-testid=pick-sound]').value === '1', null, { timeout: 10000 })
        await page.selectOption('[data-testid=pick-sound]', '0')
      })
    } finally {
      const r = await recorded()
      console.log(`      (waited ${waits.join(', ') || 'for nothing yet'}; lists of chunks asked for: ${r.lists}, for ${r.films} ${r.films === 1 ? 'film' : 'films'})`)
      console.log(`      (the player, with milliseconds since the step began: ${r.player})`)
      console.log(`      (asked of the PC, from when to when: ${r.asked})`)
    }
    // Changing the sound hands the film to the player afresh. The subtitles
    // are switched back on once it plays.
    assert.equal(await page.inputValue('[data-testid=pick-subtitles]'), subtitles, 'the same subtitles are still chosen')
    await subtitleShows('Said in the second half')
  })

  await step('a page the browser puts aside and brings back goes on playing', async () => {
    // When the viewer goes to another page in the same tab, a browser may
    // keep this one exactly as it is and bring it back on Back. It says so
    // with these two events. (A browser run by a test does not reliably do
    // this, so the events are sent by hand.)
    await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })))
    await page.waitForTimeout(500) // time for a request to stop converting, had one been sent, to arrive
    assert.equal(await converters(), 1, 'the film is still open on the PC for the page that may come back')
    await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })))
    // A part of the film the player does not hold has to come from the PC.
    await page.evaluate(() => (document.querySelector('video').currentTime = 10))
    await playsBeyond(page, 10.5, 'the page came back and jumped to a part it had not fetched')
    const at = await page.evaluate(() => document.querySelector('video').currentTime)
    assert.ok(at < 20, `jumped to 10 s, now at ${at} s`)
    // A page that is closed for good still stops the conversion at once.
    await page.evaluate(() => {
      document.querySelector('video').pause()
      window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: false }))
    })
    for (let i = 0; (await converters()) > 0; i++) {
      assert.ok(i < 50, 'the film is still held open for conversion 5 seconds after its page was closed')
      await new Promise((r) => setTimeout(r, 100))
    }
    await focusByText(page, '← Back')
    await page.keyboard.press('Enter')
    await page.waitForSelector('text=Continue watching')
    // Back to how it was, so that the next film's subtitles are down to
    // what was chosen for that film.
    await showSubtitles('auto')
  })

  await step('a picture whose size is only known once the film plays raises no false alarm; a film with no picture at all is converted for this device', async () => {
    // Safari's own chunk player does not know the size of the picture when
    // it first describes the film; the size follows a moment later. The
    // other engines know it at once, so the wait is imitated in all of
    // them: the size reads as nothing until the test says otherwise.
    const sizeUnknown = () => page.evaluate(() => {
      const real = Object.getOwnPropertyDescriptor(HTMLVideoElement.prototype, 'videoWidth')
      Object.defineProperty(HTMLVideoElement.prototype, 'videoWidth', { configurable: true, get: () => 0 })
      window.sizeBecomesKnown = () => {
        Object.defineProperty(HTMLVideoElement.prototype, 'videoWidth', real)
        document.querySelector('video').dispatchEvent(new Event('resize'))
      }
    })
    const troubleReport = () => {
      const r = page.waitForRequest((q) => q.method() === 'POST' && /\/api\/hls\/[0-9a-f]+\/trouble$/.test(q.url()), { timeout: 60000 })
      r.catch(() => {}) // looked at where it is awaited
      return r
    }
    assert.deepEqual(await learned(page), [], 'before this step nothing may have been learned about this device')
    await sizeUnknown()
    let reported = troubleReport()
    await focusByText(page, 'Three Sounds Test')
    await page.keyboard.press('Enter')
    await waitPlaying(page, 0.1)
    const from = await page.evaluate(() => document.querySelector('video').currentTime)
    await playsBeyond(page, from + 0.5, 'the film whose picture size is not known yet')
    const early = await page.evaluate(() => {
      const v = document.querySelector('video')
      let played = 0
      for (let i = 0; i < v.played.length; i++) played += v.played.end(i) - v.played.start(i)
      return { played, method: v.dataset.method, alarm: document.querySelector('[data-testid=play-problem]')?.textContent || '', notice: document.querySelector('[data-testid=step-notice]')?.textContent || '' }
    })
    // (On a machine so slow that seconds of film went by before this look,
    // the alarm may rightly have been raised already.)
    if (early.played < 2.5) assert.equal(early.alarm + early.notice, '', 'no alarm while the film has only just begun to play')
    else console.log(`      (looked ${early.played.toFixed(1)} s into playing: too late to tell whether the alarm came too early)`)
    // If the size never comes, there really is no picture this browser can
    // show. After a few seconds of playing the page tells the PC, which
    // converts the picture for this device from then on, and the film is
    // started again converted.
    const told = (await reported.catch(() => assert.fail('a film that plays on with no picture must be reported to the PC'))).postDataJSON()
    assert.equal(told.evidence, 'decode', `the page reported: ${JSON.stringify(told)}`)
    await sentAs(page, 'method', 'convert', 'after the film was reported to play without a picture')
    assert.equal(await page.textContent('[data-testid=step-said]'), 'This device had trouble showing the picture, so the PC now converts the picture to a kind every device plays.')
    // The converted picture has no size either, and there is nothing easier
    // to show. The viewer is told, in words that stay.
    await page.waitForFunction(() => {
      const v = document.querySelector('video')
      if (v && v.paused && v.readyState >= 1) {
        v.muted = true
        v.play().catch(() => {})
      }
      return /no other way/.test(document.querySelector('[data-testid=play-problem]')?.textContent || '')
    }, null, { timeout: 40000, polling: 200 }).catch(async () => assert.fail(`a converted film that still shows no picture must be said to; the page says: ${await page.locator('[data-testid=play-problem]').allTextContents()}`))
    assert.match(await page.textContent('[data-testid=play-problem]'), /^This device plays this film's sound but no picture\. The PC has no other way of sending this film that would help\./)
    await page.evaluate(() => window.sizeBecomesKnown())
    await page.waitForSelector('[data-testid=play-problem]', { state: 'detached', timeout: 5000 }).catch(() => assert.fail('the alarm must go once the size of the picture is known'))
    // The change can be taken back, with the keyboard: Undo plays the film
    // the way it was and has the PC forget what it learned.
    const kept = await learned(page)
    assert.equal(kept.length, 1, `one thing was learned: ${JSON.stringify(kept)}`)
    assert.match(kept[0].text, /^This device had trouble showing one .*film, so that film is converted for it\.$/)
    await focusByText(page, 'Undo')
    await page.keyboard.press('Enter')
    await sentAs(page, 'method', early.method, 'after Undo')
    assert.match(await page.textContent('[data-testid=step-said]'), /^Undone: the film plays the way it did before, and what the app had just learned is forgotten\./)
    assert.ok(await hasFocus(page, '[data-testid=step-notice]'), 'after Undo the keyboard is on the notice')
    assert.deepEqual(await learned(page), [], 'Undo has the PC forget what it learned')
    await page.evaluate(() => document.querySelector('video').pause())
    await focusByText(page, '← Back')
    await page.keyboard.press('Enter')
    await page.waitForSelector('text=Continue watching')
    // An ordinary file knows the size of its picture from the start. For
    // those, no size still means no picture, and the PC is told at once.
    await sizeUnknown()
    reported = troubleReport()
    await focusByText(page, 'Test Clip')
    await page.keyboard.press('Enter')
    const told2 = (await reported.catch(() => assert.fail('an ordinary file with no picture must be reported as soon as it opens'))).postDataJSON()
    assert.equal(told2.evidence, 'decode', `the page reported: ${JSON.stringify(told2)}`)
    await sentAs(page, 'method', 'convert', 'after an ordinary file was reported to play without a picture')
    await page.evaluate(() => window.sizeBecomesKnown())
    await page.evaluate(() => document.querySelector('video').pause())
    await focusByText(page, '← Back')
    await page.keyboard.press('Enter')
    await page.waitForSelector('text=Continue watching')
    // What this step taught the PC is not to change how the next steps'
    // films are sent.
    await forgetAll(page)
    assert.deepEqual(await learned(page), [])
  })

  await step('a viewer can say that HDR colours look wrong on this device, keyboard only; it is listed and can be forgotten', async () => {
    // No film in this library is HDR, and none could be made that every
    // browser this test runs in plays as it is. So the PC's answer for the
    // film is changed on its way to the page: it is said to be HDR.
    // Everything else, on the page and on the PC, is the real thing.
    const answer = /\/api\/items\/[0-9a-f]+\/play$/
    const callItHDR = async (route) => {
      const res = await route.fetch()
      await route.fulfill({ response: res, json: { ...(await res.json()), hdr: true } })
    }
    await page.route(answer, callItHDR)
    try {
      await focusByText(page, 'Test Clip')
      await page.keyboard.press('Enter')
      await waitPlaying(page, 0.2)
      const before = await page.evaluate(() => ({ src: document.querySelector('video').currentSrc, method: document.querySelector('video').dataset.method }))
      // The panel under the player is opened and the button in it pressed.
      await pressUntilFocused(page, '[data-testid=how-playing] summary')
      await page.keyboard.press('Enter')
      await pressUntilFocused(page, '[data-testid=hdr-wrong]')
      assert.equal(await page.evaluate(() => document.activeElement.textContent), 'Colours look wrong on this device')
      await page.keyboard.press('Enter')
      // This PC cannot turn HDR into normal colour, and the film is not HDR
      // in truth: either way nothing changes for it, and the page says so
      // rather than starting the film again for nothing.
      await page.waitForFunction(() => /^Noted\./.test(document.querySelector('[data-testid=hdr-said]')?.textContent || ''), null, { timeout: 20000 })
        .catch(async () => assert.fail(`the page did not say the word was noted; it says: ${await page.textContent('[data-testid=hdr]')}`))
      assert.ok(await hasFocus(page, '[data-testid=hdr-wrong]'), 'the keyboard is still on the button that was pressed')
      const after = await page.evaluate(() => ({ src: document.querySelector('video').currentSrc, method: document.querySelector('video').dataset.method }))
      assert.deepEqual(after, before, 'the film that was playing is left as it is')
      const kept = await page.evaluate(async () => await (await fetch('/api/device')).json())
      assert.ok(kept.hdrWrong && kept.lessons.length === 1 && kept.lessons[0].kind === 'hdr', `the PC keeps the viewer's word: ${JSON.stringify(kept)}`)
    } finally {
      await page.unroute(answer, callItHDR)
    }
    await page.evaluate(() => document.querySelector('video').pause())
    await focusByText(page, '← Back')
    await page.keyboard.press('Enter')
    await page.waitForSelector('text=Continue watching')
    // It is listed with the other things learned, and forgotten the same way.
    await openSettings(page)
    await page.waitForSelector('[data-testid=learned-list] li')
    assert.match((await page.locator('[data-testid=learned-list] li').allTextContents()).join(' | '), /^HDR colours were reported to look wrong on this device/)
    await focusByText(page, 'Forget')
    await page.keyboard.press('Enter')
    await page.waitForSelector('[data-testid=learned-none]').catch(() => assert.fail('the viewer\'s word was still listed after Forget'))
    assert.equal(await page.evaluate(async () => (await (await fetch('/api/device')).json()).hdrWrong), false)
  })

  await step('a repackaged film resumes where it stopped, with its subtitles', async () => {
    const asked = []
    const note = (r) => /\/hls\//.test(r.url()) && asked.push(r.url().split('/').pop())
    page.on('request', note)
    await focusByText(page, 'Dolby Sound Test')
    await page.keyboard.press('Enter')
    await waitPlaying(page, 30)
    // Being put at the right place is not yet playing from it: wait for the
    // film to move on from wherever it resumed.
    const resumedAt = await page.evaluate(() => document.querySelector('video').currentTime)
    await page.waitForFunction((t) => document.querySelector('video').currentTime > t + 0.7, resumedAt, { timeout: 20000 })
    const v = await page.evaluate(() => {
      const el = document.querySelector('video')
      const held = Array.from({ length: el.buffered.length }, (_, i) => [el.buffered.start(i), el.buffered.end(i)])
      return {
        at: el.currentTime,
        from: held.length ? held[0][0] : -1,
        // A tenth of a second of slack, for an edge that falls a hair
        // beside the clock.
        covered: held.some(([a, b]) => a <= el.currentTime + 0.1 && el.currentTime <= b + 0.1),
        ranges: held.map(([a, b]) => `${a.toFixed(2)}-${b.toFixed(2)}`).join(' '),
        sub: document.querySelector('[data-testid=pick-subtitles]').value,
      }
    })
    assert.ok(resumedAt >= 30 && resumedAt < 38 && v.at < 39.5, `resumed at ${resumedAt}, now at ${v.at}`)
    page.off('request', note)
    console.log(`      (resumed at ${resumedAt.toFixed(2)} s and played on to ${v.at.toFixed(2)} s; asked for ${asked.join(' ')}; holding ${v.ranges})`)
    // In every engine: the player holds the part of the film it says it is
    // playing. Its clock alone only says where it was told to be.
    assert.ok(v.covered, `the player says it is at ${v.at} s but holds only: ${v.ranges || 'nothing'}`)
    // The first chunk fetched is the one from 30 seconds; the player must
    // place it exactly there, or everything after would be out of step.
    if (browserName === 'chromium') assert.ok(Math.abs(v.from - 30) < 0.25, `the chunk for 30 s was placed at ${v.from}`)
    assert.notEqual(v.sub, '', 'the subtitle choice was remembered')
    await page.goBack()
    await page.waitForSelector('text=Films (4)')
  })

  await step('a show: open it, finish episode 1, and episode 2 is next up', async () => {
    await focusByText(page, 'Shows (1)')
    await page.keyboard.press('Enter')
    await focusByText(page, 'Good Show')
    await page.keyboard.press('Enter')
    await page.waitForSelector('text=Season 1')
    await focusByText(page, 'Episode 1')
    await page.keyboard.press('Enter')
    await waitPlaying(page, 0.5)
    await page.evaluate(() => {
      const v = document.querySelector('video')
      v.currentTime = v.duration - 0.5
    })
    await page.waitForFunction(() => document.querySelector('video').ended, null, { timeout: 20000 })
    await page.waitForTimeout(300)
    await page.goto(base)
    await page.waitForSelector('text=Next up')
    await page.waitForSelector('text=Good Show · Season 1 · Episode 2')
  })

  await step('search finds the show without typing its whole name', async () => {
    await pressUntilFocused(page, '#search')
    await page.keyboard.type('goo')
    await page.waitForSelector('text=Search results')
    await page.waitForSelector('text=2 episodes · 1 watched')
    await page.keyboard.press(process.platform === 'darwin' ? 'Meta+A' : 'Control+A')
    await page.keyboard.press('Backspace')
    await page.waitForSelector('text=Films (4)')
  })

  // The way out for a film the browser cannot play well: a real video
  // player. Which way depends on where the browser is.
  let handedLink
  // (Without a network address this whole run is on the PC's own address,
  // where there is no "other device" to be.)
  if (!remote) console.log('  --  no network address here: what another device is offered by "Open in another player" was not checked')
  else await step('another device is offered a file for VLC and a link to copy, keyboard only', async () => {
    await focusByText(page, 'Test Clip')
    await page.keyboard.press('Enter')
    await waitPlaying(page, 0.2)
    // Stopped at a known place, which the other player must start from: in
    // the eighth second. (The middle of it, as a browser may put the film a
    // hair beside where it was told to.)
    const stopAt = (seconds) => page.evaluate((s) => {
      const v = document.querySelector('video')
      v.pause()
      v.currentTime = s + 0.5
    }, seconds).then(() => page.waitForFunction((s) => {
      const v = document.querySelector('video')
      return v.paused && !v.seeking && Math.floor(v.currentTime) === s
    }, seconds, { timeout: 20000 }))
    await stopAt(7)
    await focusByText(page, 'Open in another player')
    await page.keyboard.press('Enter')
    await page.waitForSelector('[data-testid=elsewhere-offer]')
    assert.ok(await hasFocus(page, '[data-testid=open-elsewhere]'), 'the keyboard is still on the button that was pressed')
    assert.equal(await page.locator('[data-testid=elsewhere-problem]').count(), 0, `nothing went wrong, yet the page says: ${await page.locator('[data-testid=elsewhere-problem]').allTextContents()}`)

    // The next two stops of the keyboard are the file for VLC and the link.
    await page.keyboard.press(TAB)
    assert.ok(await hasFocus(page, '[data-testid=elsewhere-vlc]'), 'the keyboard goes on to the file for VLC')
    const file = await page.evaluate(() => ({ text: document.activeElement.textContent, href: document.activeElement.getAttribute('href'), download: document.activeElement.hasAttribute('download') }))
    assert.equal(file.text, 'Download a file for VLC')
    assert.match(file.href, /^\/api\/items\/[0-9a-f]{32}\/handoff\.xspf\?at=7$/)
    assert.ok(file.download, 'the file is offered as a download')
    await page.keyboard.press(TAB)
    assert.ok(await hasFocus(page, '[data-testid=elsewhere-link]'), 'the keyboard goes on to the link')
    const link = await page.evaluate(() => {
      const el = document.activeElement
      return { value: el.value, from: el.selectionStart, to: el.selectionEnd, readOnly: el.readOnly, label: el.labels[0]?.textContent || '' }
    })
    assert.ok(link.value.startsWith(base + '/stream/'), `the link is a whole address, at the address this device uses: ${link.value.replace(/[?].*/, '')}`)
    assert.ok(link.readOnly && link.from === 0 && link.to === link.value.length, 'reaching the link selects all of it, ready to copy')
    assert.match(link.label, /copy this link/)
    // What the link is good for is said, with the lifetime the PC gave.
    const life = await page.evaluate(async (path) => (await (await fetch(path)).json()).validSeconds, file.href.replace(/\.xspf.*/, ''))
    assert.equal(life, 12 * 3600, 'a link lasts twelve hours')
    assert.match(await page.textContent('[data-testid=elsewhere-terms]'), /plays this one film without signing in, for 12 hours, and stops working when you sign out/)
    assert.equal((await fetch(link.value, { headers: { Range: 'bytes=0-9' } })).status, 206, 'the link plays with no sign-in')
    handedLink = link.value

    // The file for VLC: a playlist with the film's name, a link of the
    // same kind, and the place the film was stopped at.
    const res = await page.request.get(base + file.href)
    assert.equal(res.status(), 200)
    assert.match(res.headers()['content-type'], /^application\/xspf\+xml/)
    assert.match(res.headers()['content-disposition'], /^attachment; filename="Test Clip\.xspf"$/)
    const list = await res.text()
    assert.match(list, /<title>Test Clip<\/title>/)
    assert.match(list, /<vlc:option>start-time=7<\/vlc:option>/)
    const inList = (list.match(/<location>([^<]+)<\/location>/) || [])[1]?.replace(/&amp;/g, '&')
    assert.ok(inList && inList.startsWith(base + '/stream/'), 'the playlist holds a whole address')
    assert.equal((await fetch(inList, { headers: { Range: 'bytes=0-9' } })).status, 206, 'the link in the playlist plays with no sign-in')
    assert.equal((await fetch(base + file.href)).status, 401, 'the playlist itself is only given to someone signed in')

    if (browserName === 'chromium') {
      // Pressed for real: the file that arrives starts the film where it
      // is at that moment, which is no longer where it was when the offer
      // opened.
      await stopAt(9)
      await pressUntilFocused(page, '[data-testid=elsewhere-vlc]', 'Shift+Tab')
      const [download] = await Promise.all([page.waitForEvent('download', { timeout: 20000 }), page.keyboard.press('Enter')])
      assert.equal(download.suggestedFilename(), 'Test Clip.xspf')
      assert.match(readFileSync(await download.path(), 'utf8'), /<vlc:option>start-time=9<\/vlc:option>/)
      assert.ok(await hasFocus(page, '[data-testid=elsewhere-vlc]'), 'the keyboard is still on the download after it was pressed')
    }

    // Only the browser on the PC may start the PC's video player.
    const refused = await page.evaluate(async (path) => {
      const r = await fetch(path, { method: 'POST', headers: { 'X-Requested-With': 'localmedia', 'Content-Type': 'application/json' }, body: '{}' })
      return { status: r.status, error: (await r.json()).error }
    }, file.href.replace(/handoff\.xspf.*/, 'open-local'))
    assert.equal(refused.status, 403)
    assert.match(refused.error, /from the browser on that PC/)
    assert.ok(!existsSync(openedInPlayer), 'nothing was opened on the PC for another device')

    // A second press puts the offer away, and the keyboard stays put.
    await focusByText(page, 'Open in another player')
    await page.keyboard.press('Enter')
    await page.waitForSelector('[data-testid=elsewhere-offer]', { state: 'detached' })
    assert.ok(await hasFocus(page, '[data-testid=open-elsewhere]'))
    await focusByText(page, '← Back')
    await page.keyboard.press('Enter')
    await page.waitForSelector('text=Continue watching')
  })

  await step('on the PC, a film this browser cannot play is opened in the PC\'s own video player, keyboard only', async () => {
    // The browser on the PC is made out to play nothing at all, which is
    // how a film it cannot play looks to the PC.
    const say = (caps) => setupPage.evaluate(async (c) => (await fetch('/api/capabilities', {
      method: 'POST', headers: { 'X-Requested-With': 'localmedia', 'Content-Type': 'application/json' }, body: JSON.stringify(c),
    })).status, caps)
    assert.equal(await say({ containers: {}, video: {}, audio: {}, hls: false }), 200)
    await focusByText(setupPage, 'Test Clip')
    await setupPage.keyboard.press('Enter')
    // The page says the film does not play here, and shows no player.
    await setupPage.waitForSelector('[data-testid=play-problem]')
    assert.equal(await setupPage.locator('video').count(), 0, 'there is no player on the page')
    await focusByText(setupPage, 'Open in another player')
    await setupPage.keyboard.press('Enter')
    await setupPage.waitForFunction(() => /Opened in this PC's video player/.test(document.querySelector('[data-testid=elsewhere-said]')?.textContent || ''), null, { timeout: 20000 })
      .catch(async () => assert.fail(`the page did not say the film was opened; it says: ${await setupPage.textContent('[data-testid=elsewhere]')}`))
    assert.ok(await hasFocus(setupPage, '[data-testid=open-elsewhere]'), 'the keyboard is still on the button that was pressed')
    // On the PC there is nothing to download or copy: the film is open.
    assert.equal(await setupPage.locator('[data-testid=elsewhere-offer]').count(), 0)
    // What the PC was asked to open: the film's own file, and nothing else.
    assert.equal(readFileSync(openedInPlayer, 'utf8'), join(media, 'Test Clip.mp4') + '\n')
    // Back to a browser that says what it really plays.
    await setupPage.goto(local)
    await setupPage.waitForSelector('text=Films (4)')
    await setupPage.waitForFunction(async () => Object.keys((await (await fetch('/api/diagnostics')).json()).device.containers).length > 0, null, { timeout: 10000 })
  })

  await step('the stream link stops working after signing out', async () => {
    const link = await page.evaluate(async () => {
      const items = (await (await fetch('/api/movies')).json()).items
      const play = async (title) => {
        const it = items.find((x) => x.title === title)
        const r = await fetch(`/api/items/${it.id}/play`, { method: 'POST', headers: { 'X-Requested-With': 'localmedia', 'Content-Type': 'application/json' }, body: '{}' })
        return (await r.json()).url
      }
      return { file: await play('Test Clip'), chunks: await play('Dolby Sound Test') }
    })
    assert.match(link.file, /^\/stream\//)
    assert.match(link.chunks, /^\/hls\//)
    assert.equal((await fetch(base + link.file, { headers: { Range: 'bytes=0-9' } })).status, 206, 'the file link works with no cookie')
    assert.equal((await fetch(base + link.chunks)).status, 200, 'the chunk list works with no cookie')
    await focusByText(page, 'Sign out')
    await page.keyboard.press('Enter')
    await page.waitForSelector('text=Sign in to Local Media')
    assert.equal((await fetch(base + link.file)).status, 403)
    assert.equal((await fetch(base + link.chunks)).status, 403)
    if (handedLink) assert.equal((await fetch(handedLink)).status, 403, 'the link given for another player ends with the sign-in too')
  })

  await step('the support bundle downloads for the owner', async () => {
    const res = await setupPage.request.get(local + '/api/support-bundle')
    assert.equal(res.status(), 200)
    const body = await res.body()
    assert.equal(body.subarray(0, 2).toString(), 'PK')
    assert.ok(!body.includes(Buffer.from('a good password')))
  })

  const real = consoleErrors.filter((e) => !/401|403|Failed to load resource/.test(e))
  assert.deepEqual(real, [], 'no script errors in the page')
  console.log(`\nAll ${steps.length} steps passed in ${browserName}.`)
} catch (err) {
  failed = true
  console.error('\nFAILED after: ' + (steps[steps.length - 1] || '(start)'))
  console.error(err)
  whenFailed()
  console.error('\n--- server log ---\n' + serverLog.replace(/\d+\.\d+\.\d+\.\d+/g, '[address]'))
} finally {
  if (browser) await browser.close()
  server.kill()
  await new Promise((r) => setTimeout(r, 300))
  for (const d of [data, media]) rmSync(d, { recursive: true, force: true })
}
process.exit(failed ? 1 : 0)
