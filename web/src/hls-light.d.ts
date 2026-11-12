// The smaller build of the chunk player: the same code without the parts
// this app does not use (its own subtitle and alternate-sound handling).
declare module 'hls.js/light' {
  export * from 'hls.js'
  export { default } from 'hls.js'
}
