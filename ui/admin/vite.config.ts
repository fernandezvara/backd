import { fileURLToPath, URL } from 'node:url'
import VueI18n from '@intlify/unplugin-vue-i18n/vite'
import tailwindcss from '@tailwindcss/vite'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vitest/config'

// The build goes straight into the directory the Go binary embeds.
export default defineConfig({
  base: '/_ui/',
  plugins: [
    vue(),
    tailwindcss(),
    // Messages are compiled at build time and the runtime-only vue-i18n is
    // used, so nothing needs eval (the CSP has no unsafe-eval).
    VueI18n({ include: [fileURLToPath(new URL('./src/i18n/*.json', import.meta.url))], runtimeOnly: true, compositionOnly: true, strictMessage: true }),
  ],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  build: {
    outDir: '../../internal/adminui/dist',
    emptyOutDir: false, // `npm run clean` does it, keeping .gitkeep
    assetsInlineLimit: 0, // CSP: no data: scripts or styles
    cssCodeSplit: false,
  },
  server: {
    // `npm run dev` against a backd running on :8080 (BACKD_ADMIN_UI not needed)
    proxy: { '/v1': 'http://localhost:8080' },
  },
  test: { environment: 'jsdom', include: ['src/**/*.test.ts'] },
})
