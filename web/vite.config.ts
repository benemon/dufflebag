import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

const patternflyLayer = {
  name: 'patternfly-cascade-layer',
  enforce: 'pre' as const,
  transform(code: string, id: string) {
    const path = id.split('?', 1)[0]
    if (!path.endsWith('.css') || !path.includes('/node_modules/@patternfly/')) return
    return `@layer patternfly {\n${code}\n}`
  },
}

// Built into dist/build/, which the Go binary embeds (ADR-0012: one container image).
// base is absolute: a relative base breaks hard loads of nested client routes
// (/buckets/x/versions/y resolves ./assets two segments deep, 404s the bundle
// and white-screens), and dufflebag can never serve under a path prefix anyway
// — the Packer SDK assigns /oauth2/token onto the auth URL, so the deployment
// guide pins the console to the hostname root (duf-o0ou.7 smoke finding).
export default defineConfig({
  plugins: [patternflyLayer, react()],
  base: '/',
  build: { outDir: 'dist/build', emptyOutDir: true },
  server: {
    // Dev-only: the API and the UI are different origins until the Go binary
    // serves the built assets. Proxying keeps every request same-origin from
    // the browser's point of view, so no CORS policy is needed here or in
    // production.
    //
    // secure:false because dufflebag serves a lab-CA certificate that Node does
    // not trust by default; the browser does, since the CA is in the system
    // store. This affects the dev proxy only.
    proxy: {
      '/oauth2': { target: 'https://localhost:8443', changeOrigin: true, secure: false },
      '/packer': { target: 'https://localhost:8443', changeOrigin: true, secure: false },
      '/api': { target: 'https://localhost:8443', changeOrigin: true, secure: false },
      '/sys': { target: 'https://localhost:8443', changeOrigin: true, secure: false },
    },
  },
})
