import { defineConfig } from 'vite'
import { resolve } from 'path'

export default defineConfig({
  publicDir: 'public-lite',
  build: {
    target: 'es2018',
    emptyOutDir: false,
  },
  resolve: {
    alias: {
      '@': resolve(__dirname, 'src')
    }
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:5656',
        changeOrigin: true
      },
      '/ws': {
        target: 'ws://127.0.0.1:5656',
        ws: true
      }
    }
  }
})
