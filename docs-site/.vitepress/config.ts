import { existsSync } from 'node:fs'
import { defineConfig } from 'vitepress'

// Grouped to match the landing page, so the sidebar carries the same taxonomy
// a reader saw on arrival.
const sidebar = [
  {
    text: 'Quick Start',
    items: [
      { text: 'Installation', link: '/quick-start/installation' },
      { text: 'Bootstrap', link: '/quick-start/bootstrap' },
      { text: 'Build an image with Packer', link: '/quick-start/build-with-packer' },
      { text: 'Manage dufflebag with Terraform', link: '/quick-start/manage-with-terraform' },
      { text: 'Upgrading', link: '/quick-start/upgrading' },
    ],
  },
  {
    text: 'Components',
    items: [
      { text: 'Architecture', link: '/components/architecture' },
      { text: 'The console', link: '/components/console' },
      { text: 'Database', link: '/components/database' },
      { text: 'Backup and restore', link: '/components/backup-restore' },
      { text: 'Object storage', link: '/components/object-storage' },
      { text: 'Encryption', link: '/components/encryption' },
      { text: 'Vulnerability scanning', link: '/components/vulnerability-scanning' },
    ],
  },
  {
    text: 'Administration',
    items: [
      { text: 'Principals', link: '/administration/principals' },
      { text: 'Audit', link: '/administration/audit' },
      { text: 'Encryption', link: '/administration/encryption' },
      { text: 'Bag Drop', link: '/administration/bag-drop' },
      { text: 'Webhooks', link: '/administration/webhooks' },
      { text: 'Instance', link: '/administration/instance' },
    ],
  },
  {
    text: 'Operations',
    items: [
      { text: 'Buckets', link: '/operations/buckets' },
      { text: 'Versions', link: '/operations/versions' },
      { text: 'Channels', link: '/operations/channels' },
      { text: 'Builds', link: '/operations/builds' },
    ],
  },
  {
    text: 'Integrations',
    items: [
      { text: 'MCP server', link: '/integrations/mcp-server' },
    ],
  },
  {
    text: 'Reference',
    items: [
      { text: 'Compatibility', link: '/reference/compatibility' },
      { text: 'Platform API', link: '/reference/platform-api' },
    ],
  },
]

for (const group of sidebar) {
  for (const item of group.items) {
    const page = item.link.endsWith('/') ? `${item.link}index` : item.link
    if (!existsSync(new URL(`..${page}.md`, import.meta.url))) {
      throw new Error(`Sidebar page does not exist: ${item.link}`)
    }
  }
}

export default defineConfig({
  title: 'dufflebag',
  description: 'Documentation for the dufflebag registry.',
  base: '/dufflebag/',
  ignoreDeadLinks: false,
  head: [
    // Base-path prefixed: '/dufflebag/' is the site root, so a bare
    // '/favicon-32.png' 404s. The dark tile is served by prefers-color-scheme.
    ['link', { rel: 'icon', type: 'image/png', sizes: '32x32', href: '/dufflebag/favicon-32.png' }],
    ['link', { rel: 'icon', type: 'image/png', sizes: '16x16', href: '/dufflebag/favicon-16.png' }],
    ['link', { rel: 'icon', type: 'image/png', sizes: '32x32', href: '/dufflebag/favicon-32-dark.png', media: '(prefers-color-scheme: dark)' }],
    ['link', { rel: 'apple-touch-icon', sizes: '180x180', href: '/dufflebag/favicon-180.png' }],
    ['meta', { name: 'theme-color', content: '#0d2c56' }],
    ['meta', { property: 'og:title', content: 'dufflebag' }],
    ['meta', { property: 'og:description', content: 'A self-hosted registry for Packer builds. One record of every build, with named channels that say which version each environment should consume.' }],
    // Absolute URL: social scrapers do not resolve the base path. The card
    // image itself is still to be produced.
    ['meta', { property: 'og:image', content: 'https://benemon.github.io/dufflebag/social-card.png' }],
    ['meta', { name: 'twitter:card', content: 'summary_large_image' }],
  ],
  themeConfig: {
    // VitePress prepends the base; no /dufflebag/ prefix here (unlike the raw
    // head links). An <img> logo cannot follow the theme via CSS, so the dark
    // variant is a separate white-ink lockup, matching the console masthead.
    logo: {
      light: '/dufflebag-logo-horizontal.svg',
      dark: '/dufflebag-logo-horizontal-inverse.svg',
    },
    // The lockup already carries the wordmark; a second title text would
    // render "dufflebag" twice.
    siteTitle: false,
    nav: [
      { text: 'Overview', link: '/' },
      { text: 'API Reference', link: '/platform-api.html', target: '_self' },
      { text: 'Compatibility', link: '/reference/compatibility' },
      { text: 'GitHub', link: 'https://github.com/benemon/dufflebag' },
    ],
    // One array, not per-section, so the sidebar is present on the landing
    // page too rather than springing in on first navigation.
    sidebar,
  },
})
