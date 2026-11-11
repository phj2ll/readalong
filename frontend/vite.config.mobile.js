import { defineConfig } from 'vite';
import { renameSync, existsSync } from 'node:fs';

// 手机端（Capacitor）专用构建：把纯前端 Reader 作为首页打进 dist-mobile。
// 桌面 Studio 的入口（index.html）依赖 Wails 注入，不能进手机包。
export default defineConfig({
  plugins: [
    {
      // Capacitor 的 WebView 默认加载 index.html，构建完把 reader.html 改名过去
      name: 'mobile-index',
      closeBundle() {
        const from = 'dist-mobile/reader.html';
        if (existsSync(from)) renameSync(from, 'dist-mobile/index.html');
      },
    },
  ],
  build: {
    outDir: 'dist-mobile',
    emptyOutDir: true,
    target: 'chrome105',
    rollupOptions: {
      input: {
        index: 'reader.html',
      },
    },
  },
});
