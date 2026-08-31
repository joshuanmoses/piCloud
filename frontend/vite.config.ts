// defineConfig comes from vitest/config so the `test` block is typed.
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { fileURLToPath, URL } from 'node:url'

// The dev server proxies the API to the Go backend so the browser sees a single
// origin. That keeps cookies, CSRF, and CORS behaving in development exactly as
// they do in production, where Caddy fronts both.
const BACKEND = process.env.VITE_BACKEND_URL ?? 'http://localhost:8080'

export default defineConfig({
  plugins: [react(), tailwindcss()],

  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },

  server: {
    host: true, // reachable from another device on the LAN, e.g. a real Pi
    port: 5173,
    proxy: {
      '/api': { target: BACKEND, changeOrigin: false },
      '/health': { target: BACKEND, changeOrigin: false },
      // Public share and upload-request portals are served by the backend.
      '/s': { target: BACKEND, changeOrigin: false },
      '/u': { target: BACKEND, changeOrigin: false },
    },
  },

  build: {
    outDir: 'dist',
    // Raspberry Pi clients are frequently on slow local networks and modest
    // hardware. A budget that fails the build keeps the bundle honest.
    chunkSizeWarningLimit: 400,
    sourcemap: false,
    target: 'es2022',
    rollupOptions: {
      output: {
        // Split the rarely-changing vendor code out of the app chunk so an
        // application update does not invalidate it in the browser cache.
        //
        // Matching on the resolved module path rather than the bare package
        // name is deliberate: `react-dom/client` and `react/jsx-runtime` are
        // distinct module ids, and a name-based map silently leaves them in
        // the application chunk.
        manualChunks(id) {
          if (!id.includes('node_modules')) return undefined
          if (/[\\/]node_modules[\\/](react|react-dom|scheduler)[\\/]/.test(id)) return 'react'
          if (id.includes('react-router')) return 'router'
          if (id.includes('@tanstack')) return 'query'
          return 'vendor'
        },
      },
    },
  },

  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    css: false,
    restoreMocks: true,
    coverage: {
      reporter: ['text', 'html'],
      include: ['src/**/*.{ts,tsx}'],
      exclude: ['src/test/**', 'src/**/*.test.{ts,tsx}', 'src/main.tsx', 'src/types/**'],
    },
  },
})
