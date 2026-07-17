/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'

// 后端地址可由环境变量覆盖：净环境自检 / CI 上可能不是 :8280
const BACKEND = process.env.BACKEND_URL ?? 'http://localhost:8280'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  test: {
    // node 环境：现有单测是纯逻辑（插值契约），要读仓库根的 golden 夹具。
    // 将来加组件测试时再引 jsdom。
    environment: 'node',
    include: ['src/**/*.test.ts'],
    // e2e/ 归 Playwright（它的 .spec.ts 用的是 @playwright/test），别让 vitest 也去捡。
    exclude: ['e2e/**', 'node_modules/**', 'dist/**'],
  },
  server: {
    proxy: {
      '/api': {
        target: BACKEND,
        changeOrigin: true,
        rewrite: (p) => p.replace(/^\/api/, ''),
      },
    },
  },
  // 生产构建本地预览（vite preview）也代理 /api → 后端
  preview: {
    proxy: {
      '/api': {
        target: BACKEND,
        changeOrigin: true,
        rewrite: (p) => p.replace(/^\/api/, ''),
      },
    },
  },
})
