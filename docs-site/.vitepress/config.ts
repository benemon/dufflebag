import { existsSync } from 'node:fs'
import { defineConfig } from 'vitepress'

// Grouped to match the landing page, so the sidebar carries the same taxonomy
// a reader saw on arrival.
const sidebar = [
  {
    text: 'Getting started',
    items: [
      { text: 'Installation', link: '/getting-started/installation' },
      { text: 'First use', link: '/getting-started/first-use' },
    ],
  },
  {
    text: 'Administration',
    items: [
      { text: 'Roles & principals', link: '/administration/roles-principals' },
      { text: 'The console', link: '/administration/console' },
      { text: 'Audit', link: '/administration/audit' },
      { text: 'Encryption', link: '/administration/encryption' },
      { text: 'Bag Drop', link: '/administration/bag-drop' },
      { text: 'Webhooks', link: '/administration/webhooks' },
    ],
  },
  {
    text: 'Using the registry',
    items: [
      { text: 'Terraform provider', link: '/using/terraform' },
      { text: 'Revocation & channels', link: '/using/revocation-channels' },
      { text: 'SBOMs & findings', link: '/using/sbom-findings' },
    ],
  },
  {
    text: 'Deployment',
    items: [
      { text: 'Deploying dufflebag', link: '/deployment/' },
      { text: 'Object storage', link: '/deployment/object-storage' },
      { text: 'Encryption setup', link: '/deployment/encryption-setup' },
      { text: 'Operations', link: '/deployment/operations' },
    ],
  },
  {
    text: 'Reference',
    items: [
      { text: 'Architecture', link: '/reference/architecture' },
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
      { text: 'Guides', link: '/getting-started/installation' },
      { text: 'API Reference', link: '/platform-api.html' },
      { text: 'Compatibility', link: '/reference/compatibility' },
    ],
    sidebar: {
      '/getting-started/': sidebar,
      '/administration/': sidebar,
      '/using/': sidebar,
      '/deployment/': sidebar,
      '/reference/': sidebar,
    },
  },
})
