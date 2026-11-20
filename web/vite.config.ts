import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig(({ mode }) => ({
  plugins: [sveltekit({ adapter: adapter() })],
  optimizeDeps: mode === 'test' ? { include: ['@ui-sdk/svelte/agent'] } : undefined,
  server: {
    fs: mode === 'test' ? { allow: ['test'] } : undefined,
    proxy: {
      '/api': 'http://127.0.0.1:8080'
    }
  }
}));
