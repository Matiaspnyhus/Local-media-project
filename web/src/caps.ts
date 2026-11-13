import type { Caps } from './api'

// What this browser says it can play. It is asked directly rather than
// guessed from the browser's name. The known cases where a browser claims
// more than it delivers are corrected afterwards: most by the server, and
// one here, where the page already knows whose browser it is running in.
export async function measureCaps(): Promise<Caps> {
  const caps = askedDirectly()
  const tall = await measureTall(caps.video)
  if (tall) caps.tall = tall
  return caps
}

// What the browser answers at once.
function askedDirectly(): Caps {
  const v = document.createElement('video')
  const can = (...types: string[]) => types.some((t) => v.canPlayType(t) !== '')
  const mp4 = can('video/mp4')
  return {
    hls: nativeHLS() || canUseChunkPlayer(),
    containers: {
      mp4,
      mov: mp4 || can('video/quicktime'),
      matroska: can('video/x-matroska', 'video/x-matroska; codecs="avc1.640028"'),
      webm: can('video/webm'),
    },
    video: {
      h264: can('video/mp4; codecs="avc1.640028"'),
      // H.264 with 10-bit colour is asked about separately: a browser that
      // plays ordinary H.264 may or may not play it (Chrome does). Apple's
      // browsers answer yes, but are widely reported to show no picture for
      // such a file, and this has not been tried on a real iPhone. Until
      // it has, their answer is not passed on: the PC then says plainly
      // that this device cannot play the file, with "Try anyway" beside
      // it, where otherwise the film might open to sound and no picture.
      'h264-10': !appleBrowser() && can('video/mp4; codecs="avc1.6E0033"'),
      // HEVC is labelled in a file in one of two ways, "hvc1" or "hev1".
      // "hevc" and "hevc10" (10-bit colour) say the browser plays HEVC in
      // at least one of them. "hevc-hev1" says it plays the hev1 labelling
      // as it is, which Apple devices, insisting on hvc1, may not. With
      // that known, the PC can repackage such a file for them, which
      // relabels it.
      hevc: can('video/mp4; codecs="hvc1.1.6.L120.90"', 'video/mp4; codecs="hev1.1.6.L120.90"'),
      hevc10: can('video/mp4; codecs="hvc1.2.4.L120.90"', 'video/mp4; codecs="hev1.2.4.L120.90"'),
      'hevc-hev1': can('video/mp4; codecs="hev1.1.6.L120.90"', 'video/mp4; codecs="hev1.2.4.L120.90"'),
      av1: can('video/mp4; codecs="av01.0.08M.08"'),
      vp9: can('video/webm; codecs="vp9"', 'video/mp4; codecs="vp09.00.40.08"'),
      vp8: can('video/webm; codecs="vp8"'),
      // Dolby Vision of the kind (profile 5) that looks green and purple
      // on a device that plays it as ordinary HEVC.
      dovi5: can('video/mp4; codecs="dvh1.05.06"'),
    },
    audio: {
      aac: can('audio/mp4; codecs="mp4a.40.2"'),
      mp3: can('audio/mpeg'),
      ac3: can('audio/mp4; codecs="ac-3"'),
      eac3: can('audio/mp4; codecs="ec-3"'),
      opus: can('audio/webm; codecs="opus"', 'audio/mp4; codecs="opus"', 'audio/ogg; codecs="opus"'),
      flac: can('audio/flac', 'audio/mp4; codecs="flac"'),
      vorbis: can('audio/webm; codecs="vorbis"', 'audio/ogg; codecs="vorbis"'),
    },
    pageFullscreen: pageFullscreen(),
  }
}

// pageFullscreen: the page itself can fill the screen, so whatever it draws
// over a film is still there in fullscreen. An iPhone lets only the video
// do that.
function pageFullscreen(): boolean {
  const d = document as unknown as { fullscreenEnabled?: boolean; webkitFullscreenEnabled?: boolean }
  return !!(d.fullscreenEnabled || d.webkitFullscreenEnabled)
}

// The picture formats, each as a 4K film of that format would be labelled.
const tallTypes: Record<string, string[]> = {
  h264: ['video/mp4; codecs="avc1.640033"'],
  'h264-10': ['video/mp4; codecs="avc1.6E0033"'],
  hevc: ['video/mp4; codecs="hvc1.1.6.L153.90"', 'video/mp4; codecs="hev1.1.6.L153.90"'],
  hevc10: ['video/mp4; codecs="hvc1.2.4.L153.90"', 'video/mp4; codecs="hev1.2.4.L153.90"'],
  av1: ['video/mp4; codecs="av01.0.12M.08"'],
  vp9: ['video/webm; codecs="vp09.00.50.08"', 'video/mp4; codecs="vp09.00.50.08"'],
  vp8: ['video/webm; codecs="vp8"'],
}

// measureTall asks the browser, for each picture format it plays, whether
// it would play a 4K film of that format smoothly. A phone or an old PC
// may open such a film and then stutter through it; the PC can make a
// 1080p picture for it instead. The answer is undefined when the browser
// has no way to ask, or does not answer in a few seconds.
async function measureTall(video: Record<string, boolean>): Promise<Record<string, boolean> | undefined> {
  const mc = (navigator as unknown as { mediaCapabilities?: MediaCapabilities }).mediaCapabilities
  if (!mc || typeof mc.decodingInfo !== 'function') return undefined
  const smooth = async (contentType: string) => {
    try {
      const r = await mc.decodingInfo({ type: 'file', video: { contentType, width: 3840, height: 2160, bitrate: 25_000_000, framerate: 24 } })
      return r.supported && r.smooth
    } catch {
      return false // a format the browser does not know the name of
    }
  }
  const all = (async () => {
    const tall: Record<string, boolean> = {}
    for (const [key, types] of Object.entries(tallTypes)) {
      if (!video[key]) continue
      tall[key] = (await Promise.all(types.map(smooth))).some(Boolean)
    }
    return tall
  })()
  const late = new Promise<undefined>((done) => setTimeout(() => done(undefined), 4000))
  return Promise.race([all, late])
}

// nativeHLS: the browser plays a list of chunks by itself (Safari, and
// every browser on an iPhone or iPad).
export function nativeHLS(): boolean {
  return document.createElement('video').canPlayType('application/vnd.apple.mpegurl') !== ''
}

// canUseChunkPlayer: the browser lets a page feed video to the player piece
// by piece, which is how the built-in chunk player (hls.js) works.
export function canUseChunkPlayer(): boolean {
  const w = window as unknown as { MediaSource?: unknown; ManagedMediaSource?: unknown }
  return typeof w.MediaSource !== 'undefined' || typeof w.ManagedMediaSource !== 'undefined'
}

// builtInChunks says whether to let the browser play a list of chunks by
// itself. On Apple devices that is the well-worn path (and on older iPhones
// the only one). Elsewhere the app's own player is used wherever it can
// run: some other browsers have begun to play chunk lists themselves, but
// that is newer and less proven than the player this app is tested with.
export function builtInChunks(): boolean {
  if (!nativeHLS()) return false
  return appleBrowser() || !canUseChunkPlayer()
}

// appleBrowser: Safari on a Mac, or any browser on an iPhone or iPad (they
// are all Safari underneath, whatever their name).
function appleBrowser(): boolean {
  return /Apple/.test(navigator.vendor || '')
}
