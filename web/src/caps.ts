import type { Caps } from './api'

// What this browser says it can play. It is asked directly rather than
// guessed from the browser's name; the server corrects the known cases
// where a browser claims more than it delivers.
export function measureCaps(): Caps {
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
      // plays ordinary H.264 may or may not play it (Chrome does).
      'h264-10': can('video/mp4; codecs="avc1.6E0033"'),
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
  }
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
  return /Apple/.test(navigator.vendor || '') || !canUseChunkPlayer()
}
