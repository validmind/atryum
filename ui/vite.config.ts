import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import type { Plugin } from 'vite';

const spaFallback = (): Plugin => ({
  name: 'atryum-spa-fallback',
  configureServer(server) {
    server.middlewares.use((req, _res, next) => {
      const url = req.url ?? '/';
      if (
        !url.includes('.') &&
        !url.startsWith('/@') &&
        !url.startsWith('/api/') &&
        !url.startsWith('/mcp/')
      ) {
        req.url = '/index.html';
      }
      next();
    });
  },
});

export default defineConfig({
  base: '/ui/',
  plugins: [react(), spaFallback()],
  server: {
    host: true,
    port: 5174,
    proxy: {
      '/api': {
        target: process.env.ATRYUM_BACKEND_URL ?? 'http://localhost:8080',
        changeOrigin: true,
      },
      '/mcp': {
        target: process.env.ATRYUM_BACKEND_URL ?? 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: false,
    minify: 'esbuild',
    emptyOutDir: true,
  },
});
