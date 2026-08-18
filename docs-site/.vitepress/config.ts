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
  title: 'Dufflebag',
  description: 'Documentation for the dufflebag registry.',
  base: '/dufflebag/',
  ignoreDeadLinks: false,
  themeConfig: {
    nav: [
      { text: 'Overview', link: '/' },
      { text: 'API Reference', link: '/platform-api.html', target: '_self' },
      { text: 'Compatibility', link: '/reference/compatibility' },
      { text: 'GitHub', link: 'https://github.com/benemon/dufflebag' },
    ],
    sidebar: {
      '/quick-start/': sidebar,
      '/components/': sidebar,
      '/administration/': sidebar,
      '/operations/': sidebar,
      '/integrations/': sidebar,
      '/reference/': sidebar,
    },
  },
})
