import { defineConfig } from 'vite';

export default defineConfig({
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'chrome105',
    rollupOptions: {
      input: {
        main: 'index.html', // Studio（桌面版）
        reader: 'reader.html', // Reader（纯前端阅读端）
      },
    },
  },
  server: {
    port: 34115,
    strictPort: false,
  },
});
