import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

export default defineConfig({
  plugins: [svelte()],
  base: '/admin/ui/',
  build: {
    outDir: '../internal/ui/dist',
    emptyOutDir: true
  },
  server: {
    proxy: {
      '/admin': {
        target: 'http://127.0.0.1:18080',
        changeOrigin: true
      }
    }
  }
});
