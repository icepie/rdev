import { defineConfig } from 'vite';
import { resolve } from 'node:path';

export default defineConfig({
  root: 'web',
  base: '/',
  build: {
    outDir: '../internal/server/static',
    emptyOutDir: false,
    sourcemap: false,
    rollupOptions: {
      input: {
        index: resolve(__dirname, 'web/index.html'),
        terminal: resolve(__dirname, 'web/terminal.html'),
        desktop: resolve(__dirname, 'web/desktop.html'),
        batch: resolve(__dirname, 'web/batch.html'),
        files: resolve(__dirname, 'web/files.html'),
        sessions: resolve(__dirname, 'web/sessions.html')
      }
    }
  }
});
