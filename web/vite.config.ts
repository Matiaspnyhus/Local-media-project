import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The oldest browsers the app must run in are fixed by the plan:
// Chromium 87 (2022 LG TVs) and Safari 15.
export default defineConfig({
  plugins: [react()],
  build: { target: ['chrome87', 'safari15'], outDir: 'dist', emptyOutDir: true, sourcemap: false },
  server: { proxy: { '/api': 'http://localhost:8177', '/stream': 'http://localhost:8177', '/health': 'http://localhost:8177' } },
})
