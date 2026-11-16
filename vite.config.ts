import { defineConfig, type Plugin } from 'vite';
import { resolve } from 'node:path';
import { writeFile } from 'node:fs/promises';
import { brotliCompressSync, constants, gzipSync } from 'node:zlib';
import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';

// Emit portable sidecars into the actual output directory, including --outDir
// overrides. Go embeds these as ordinary files; production needs no compressor.
function precompressedAssets(): Plugin {
  return {
    name: 'precompressed-assets',
    apply: 'build',
    async writeBundle(output, bundle) {
      if (!output.dir) throw new Error('Static compression requires an output directory');
      await Promise.all(Object.values(bundle).map(async (entry) => {
        if (!/\.(?:html|css|m?js|json|svg|xml|txt|wasm)$/.test(entry.fileName)) return;
        const source = entry.type === 'chunk' ? entry.code : entry.source;
        const data = typeof source === 'string' ? Buffer.from(source) : source;
        const representations = [
          ['gz', gzipSync(data, { level: 9 })],
          ['br', brotliCompressSync(data, {
            params: { [constants.BROTLI_PARAM_QUALITY]: 11 },
          })],
        ] as const;
        await Promise.all(representations.map(async ([suffix, compressed]) => {
          if (compressed.length < data.length) {
            await writeFile(resolve(output.dir!, `${entry.fileName}.${suffix}`), compressed);
          }
        }));
      }));
    },
  };
}

export default defineConfig({
  root: 'web',
  base: '/',
  plugins: [react(), tailwindcss(), precompressedAssets()],
  build: {
    outDir: '../internal/server/static',
    emptyOutDir: true,
    sourcemap: false,
    rolldownOptions: {
      output: {
        // The server's immutable policy recognizes this exact hash shape.
        entryFileNames: 'assets/[name]-[hash:8].js',
        chunkFileNames: 'assets/[name]-[hash:8].js',
        assetFileNames: 'assets/[name]-[hash:8][extname]',
        // Keep the framework hash stable across application-only releases.
        codeSplitting: { groups: [{ name: 'framework', test: /node_modules\/(?:react|react-dom|scheduler)\// }] },
      },
      input: {
        app: resolve(import.meta.dirname, 'web/app.html')
      }
    }
  }
});
