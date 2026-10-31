/// <reference types="vitest/config" />
import { fileURLToPath } from 'node:url'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// Libraries get chunks of their own, which browsers keep cached across releases of the panel.
const vendors = {
  react: /node_modules[\\/](react|react-dom|scheduler)[\\/]/,
  tanstack: /node_modules[\\/]@tanstack[\\/]/,
}

// During development the master runs on :8080 and serves the API (`noryx-master serve`).
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  server: { proxy: { '/api': 'http://127.0.0.1:8080' } },
  build: {
    // Assets are never inlined as data: URIs, so the CSP can stay strict.
    assetsInlineLimit: 0,
    rolldownOptions: {
      output: { codeSplitting: { groups: Object.entries(vendors).map(([name, test]) => ({ name, test })) } },
    },
  },
  // `npm test` runs the tests next to the modules in Node, in a time zone with daylight saving time.
  test: {
    include: ['src/**/*.test.{ts,tsx}'],
    setupFiles: ['src/test-setup.ts'],
    env: { TZ: 'Europe/Berlin' },
    unstubGlobals: true,
    restoreMocks: true,
  },
})
