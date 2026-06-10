import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  base: '/ui/',
  plugins: [react()],
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
    rollupOptions: {
      output: {
        entryFileNames: 'assets/[name].js',
        chunkFileNames: 'assets/[name].js',
        assetFileNames: 'assets/[name][extname]',
      },
    },
  },
});
