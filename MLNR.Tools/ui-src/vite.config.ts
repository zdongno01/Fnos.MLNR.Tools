import { defineConfig } from 'vite'
import { resolve } from 'path'

// 生产环境：前端由 Go 后端托管，资源统一前缀 /app/mlnr
// 开发环境：Vite 代理 /app/mlnr/api 与 /app/mlnr/ws 到 Go 后端 (127.0.0.1:8088)
export default defineConfig({
  base: '/app/mlnr/',
  resolve: {
    alias: { '@': resolve(__dirname, 'src') },
  },
  build: {
    outDir: '../app/ui',
    emptyOutDir: false,
    assetsDir: 'assets',
    rollupOptions: {
      output: {
        entryFileNames: 'assets/[name]-[hash].js',
        chunkFileNames: 'assets/[name]-[hash].js',
        assetFileNames: 'assets/[name]-[hash].[ext]',
      },
    },
  },
  server: {
    port: 5180,
    proxy: {
      '/app/mlnr/api': { target: 'http://127.0.0.1:8088', changeOrigin: true },
      '/app/mlnr/ws': { target: 'ws://127.0.0.1:8088', ws: true, changeOrigin: true },
    },
  },
})
