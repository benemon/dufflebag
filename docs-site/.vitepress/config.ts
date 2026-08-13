import { existsSync } from 'node:fs'
import { defineConfig } from 'vitepress'

// Grouped to match the landing page's three sections, so the sidebar carries
// the same taxonomy a reader saw on arrival.
const guideSidebar = [
  {
    text: 'Getting going',
    items: [
      { text: 'Installation', link: '/guides/installation' },
      { text: 'Getting started', link: '/guides/getting-started' },
      { text: 'Roles & principals', link: '/guides/roles-principals' },
    ],
  },
  {
    text: 'Day to day',
    items: [
      { text: 'The console', link: '/guides/console' },
      { text: 'Terraform provider', link: '/guides/terraform' },
      { text: 'Revocation & channels', link: '/guides/revocation-channels' },
      { text: 'SBOMs & findings', link: '/guides/sbom-findings' },
    ],
  },
  {
    text: 'Operating',
    items: [
      { text: 'Bag Drop', link: '/guides/bag-drop' },
      { text: 'Webhooks', link: '/guides/webhooks' },
      { text: 'Audit & encryption', link: '/guides/audit-encryption' },
      { text: 'Platform API', link: '/guides/platform-api' },
    ],
  },
]

for (const group of guideSidebar) {
  for (const item of group.items) {
    if (!existsSync(new URL(`..${item.link}.md`, import.meta.url))) {
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
      { text: 'Guides', link: '/guides/getting-started' },
      { text: 'API Reference', link: '/platform-api.html' },
      {
        text: 'Compatibility',
        link: 'https://github.com/benemon/dufflebag/blob/main/docs/compatibility.md',
      },
    ],
    sidebar: {
      '/guides/': guideSidebar,
    },
  },
})
