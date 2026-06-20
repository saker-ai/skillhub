import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'node:path'

export default defineConfig({
  plugins: [react()],
  base: process.env.VITE_BASE_PATH || '/',
  resolve: {
    alias: {
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
      allow: ['.', '../../web-shared'],
    },
    proxy: {
      '/api': 'http://localhost:10070',
      '/login': 'http://localhost:10070',
      '/logout': 'http://localhost:10070',
      '/.well-known': 'http://localhost:10070',
      '/healthz': 'http://localhost:10070',
    },
  },
})
