// End-to-end check of M0, driven with the keyboard only, against the real
// program. The owner's journey: set up on the PC, then sign in from another
// device over plain HTTP, play a file, leave, and resume.
//
//   node e2e/run.mjs            (Chromium)
//   E2E_BROWSER=webkit node e2e/run.mjs
//
// Needs the program built at ./localmedia (or LOCALMEDIA_BIN).
import { spawn, spawnSync } from 'node:child_process'
import { mkdtempSync, cpSync, rmSync, existsSync, mkdirSync, copyFileSync, writeFileSync } from 'node:fs'
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
const port = 18177
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

const server = spawn(bin, ['--data', data, '--port', String(port), '--no-browser'], { stdio: ['ignore', 'pipe', 'pipe'] })
let serverLog = ''
server.stdout.on('data', (d) => (serverLog += d))
server.stderr.on('data', (d) => (serverLog += d))

const local = `http://localhost:${port}`
const lan = lanAddress()
const remote = lan ? `http://${lan}:${port}` : null
let browser
let failed = false
try {
  for (let i = 0; ; i++) {
    try {
      if ((await fetch(local + '/health')).ok) break
    } catch {}
    if (i > 100) throw new Error('server did not start:\n' + serverLog)
    await new Promise((r) => setTimeout(r, 100))
  }
  console.log(`browser: ${browserName}; PC address ${local}; other-device address ${remote || '(none found, using localhost)'}`)
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
    await setupPage.waitForSelector('text=Films (2)')
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
    await page.waitForSelector('text=Films (2)')
  })

  await step('play a file, keyboard only', async () => {
    await focusByText(page, 'Test Clip')
    await page.keyboard.press('Enter')
    await waitPlaying(page, 1)
    const w = await page.evaluate(() => document.querySelector('video').videoWidth)
    assert.equal(w, 320, 'the picture is decoded')
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
    await page.waitForSelector('text=Films (2)')
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
  })

  await step('subtitles show, and are still in step after a jump far ahead', async () => {
    await pressUntilFocused(page, '[data-testid=pick-subtitles]')
    await page.keyboard.press('ArrowDown')
    if ((await page.inputValue('[data-testid=pick-subtitles]')) === '') await page.selectOption('[data-testid=pick-subtitles]', { index: 1 })
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
      return { at: el.currentTime, from: el.buffered.length ? el.buffered.start(0) : -1, sub: document.querySelector('[data-testid=pick-subtitles]').value }
    })
    assert.ok(resumedAt >= 30 && resumedAt < 38 && v.at < 39.5, `resumed at ${resumedAt}, now at ${v.at}`)
    // The first chunk fetched is the one from 30 seconds; the player must
    // place it exactly there, or everything after would be out of step.
    page.off('request', note)
    const ranges = await page.evaluate(() => {
      const b = document.querySelector('video').buffered
      return Array.from({ length: b.length }, (_, i) => `${b.start(i).toFixed(2)}-${b.end(i).toFixed(2)}`).join(' ')
    })
    console.log(`      (resumed at ${resumedAt.toFixed(2)} s and played on to ${v.at.toFixed(2)} s; asked for ${asked.join(' ')}; holding ${ranges})`)
    if (browserName === 'chromium') assert.ok(Math.abs(v.from - 30) < 0.25, `the chunk for 30 s was placed at ${v.from}`)
    assert.notEqual(v.sub, '', 'the subtitle choice was remembered')
    await page.goBack()
    await page.waitForSelector('text=Films (2)')
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
    await page.waitForSelector('text=Films (2)')
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
  console.error('\n--- server log ---\n' + serverLog.replace(/\d+\.\d+\.\d+\.\d+/g, '[address]'))
} finally {
  if (browser) await browser.close()
  server.kill()
  await new Promise((r) => setTimeout(r, 300))
  for (const d of [data, media]) rmSync(d, { recursive: true, force: true })
}
process.exit(failed ? 1 : 0)
