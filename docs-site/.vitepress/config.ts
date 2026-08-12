import { existsSync } from 'node:fs'
import { defineConfig } from 'vitepress'

const guideSidebar = [
  { text: 'Installation', link: '/guides/installation' },
  { text: 'Getting started', link: '/guides/getting-started' },
  { text: 'Roles & principals', link: '/guides/roles-principals' },
  { text: 'Revocation & channels', link: '/guides/revocation-channels' },
  { text: 'Bag Drop', link: '/guides/bag-drop' },
  { text: 'Platform API', link: '/guides/platform-api' },
]

for (const item of guideSidebar) {
  if (!existsSync(new URL(`..${item.link}.md`, import.meta.url))) {
    throw new Error(`Sidebar page does not exist: ${item.link}`)
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
      '/guides/': [
        {
          text: 'Guides',
          items: guideSidebar,
        },
      ],
    },
  },
})
