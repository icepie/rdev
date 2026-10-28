import { defineConfig } from 'vite';
import { resolve } from 'node:path';
import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  root: 'web',
  base: '/',
  plugins: [react(), tailwindcss()],
  build: {
    outDir: '../internal/server/static',
    emptyOutDir: true,
    sourcemap: false,
    rollupOptions: {
      input: {
        app: resolve(__dirname, 'web/app.html')
      }
    }
  }
});
