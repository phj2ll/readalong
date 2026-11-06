import { defineConfig } from 'vite';

export default defineConfig({
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'chrome105',
  },
  server: {
    port: 34115,
    strictPort: false,
  },
});
