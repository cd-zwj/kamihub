import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import path from 'node:path';

// 开发时把 /api、/d/、/uploads/ 与 /health 代理到本地 Go 后端；
// 生产环境由 Nginx 做同样的事（见 deploy/nginx-web.conf）。
const BACKEND = process.env.VITE_BACKEND || 'http://127.0.0.1:8080';

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: { '@': path.resolve(__dirname, 'src') },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': { target: BACKEND, changeOrigin: true },
      '/health': { target: BACKEND, changeOrigin: true },
      // 提货页。必须写成 ^/d/ 而不是 '/d'：
      // 后者是前缀匹配，会把 /docs（开放接口文档）也一并转给后端，
      // 导致文档页 500。生产 Nginx 用的是 location /d/（带斜杠），
      // 不会误伤 /docs，这里保持行为一致。
      '^/d/': { target: BACKEND, changeOrigin: true },
      // 上传的商品图片由后端托管
      '^/uploads/': { target: BACKEND, changeOrigin: true },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: false,
    chunkSizeWarningLimit: 1500,
  },
});
