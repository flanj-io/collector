import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';

// The build output is embedded by the flanjui extension (go:embed all:web/dist).
// The Dockerfile copies dist/ into ../extension/flanjui/web/dist before the Go
// build. In dev, /api/* is proxied to a locally-running collector UI (:5335).
export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: 'dist',
    emptyOutDir: true
  },
  server: {
    port: 5336,
    proxy: {
      // FLANJ_API_PROXY lets dev point at a non-default collector (or a mock).
      '/api': process.env.FLANJ_API_PROXY || 'http://127.0.0.1:5335'
    }
  }
});
