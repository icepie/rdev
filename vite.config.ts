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
    rolldownOptions: {
      output: {
        // Keep the framework hash stable across application-only releases.
        codeSplitting: { groups: [{ name: 'framework', test: /node_modules\/(?:react|react-dom|scheduler)\// }] },
      },
      input: {
        app: resolve(import.meta.dirname, 'web/app.html')
      }
    }
  }
});
