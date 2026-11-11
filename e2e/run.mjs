// End-to-end check of M0, driven with the keyboard only, against the real
// program. The owner's journey: set up on the PC, then sign in from another
// device over plain HTTP, play a file, leave, and resume.
//
//   node e2e/run.mjs            (Chromium)
//   E2E_BROWSER=webkit node e2e/run.mjs
//
// Needs the program built at ./localmedia (or LOCALMEDIA_BIN).
import { spawn } from 'node:child_process'
import { mkdtempSync, cpSync, rmSync, existsSync } from 'node:fs'
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
      assert.equal((await fetch(remote + '/api/items')).status, 403)
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
    await setupPage.waitForSelector('text=All videos (2)')
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
    assert.equal((await page.request.get(base + '/api/items')).status(), 401)
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
    await page.waitForSelector('text=All videos (2)')
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
    await focusByText(page, 'Back to the library')
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
    await page.waitForSelector('text=All videos (2)')
  })

  if (browserName === 'chromium') {
    await step('a file with Dolby sound says why it is silent', async () => {
      await focusByText(page, 'Dolby Sound Sample')
      await page.keyboard.press('Enter')
      await page.waitForSelector('video')
      await page.evaluate(() => {
        const v = document.querySelector('video')
        v.play().catch(() => {})
      })
      await page.waitForSelector('[data-testid=play-problem]', { timeout: 20000 })
      await page.goBack()
    })
  }

  await step('the stream link stops working after signing out', async () => {
    const link = await page.evaluate(async () => {
      const items = (await (await fetch('/api/items')).json()).items
      const r = await fetch(`/api/items/${items[0].id}/play`, { method: 'POST', headers: { 'X-Requested-With': 'localmedia', 'Content-Type': 'application/json' }, body: '{}' })
      return (await r.json()).url
    })
    assert.equal((await fetch(base + link, { headers: { Range: 'bytes=0-9' } })).status, 206, 'link works with no cookie')
    await focusByText(page, 'Sign out')
    await page.keyboard.press('Enter')
    await page.waitForSelector('text=Sign in to Local Media')
    assert.equal((await fetch(base + link)).status, 403)
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
