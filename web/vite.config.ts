import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'node:path'

const proxyTarget = process.env.SKILLHUB_WEB_PROXY_TARGET || 'http://localhost:10070'

export default defineConfig({
  plugins: [react()],
  base: process.env.VITE_BASE_PATH || '/',
  resolve: {
    alias: {
      '@saker/web-shared': path.resolve(__dirname, '../../web-shared/src'),
      '@saker/skillhub-client': path.resolve(__dirname, '../web-client/src'),
      react: path.resolve(__dirname, 'node_modules/react'),
      'react/jsx-runtime': path.resolve(__dirname, 'node_modules/react/jsx-runtime'),
      'lucide-react': path.resolve(__dirname, 'node_modules/lucide-react')
    },
    dedupe: ['react', 'react-dom']
  },
  build: {
    outDir: './static',
    emptyOutDir: true,
  },
  server: {
    fs: {
      allow: ['.', '../../web-shared', '../web-client'],
    },
    proxy: {
      '/api': proxyTarget,
      '/login': proxyTarget,
      '/logout': proxyTarget,
      '/.well-known': proxyTarget,
      '/healthz': proxyTarget,
    },
  },
})
