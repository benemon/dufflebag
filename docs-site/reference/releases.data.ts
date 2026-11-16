import { createMarkdownRenderer, defineLoader, type SiteConfig } from 'vitepress'

export type Release = { tag: string; date: string; url: string; notes: string }

declare const data: Release[]
export { data }

type GitHubRelease = { tag_name: string; published_at: string; html_url: string; body: string | null; draft: boolean }

// Release notes are rendered from text contributors influence (pull request
// titles), so raw HTML stays off.
export default defineLoader({
  async load(): Promise<Release[]> {
    const headers: Record<string, string> = { Accept: 'application/vnd.github+json' }
    if (process.env.GITHUB_TOKEN) headers.Authorization = `Bearer ${process.env.GITHUB_TOKEN}`
    const response = await fetch('https://api.github.com/repos/benemon/dufflebag/releases?per_page=100', { headers })
    if (!response.ok) throw new Error(`GitHub releases: ${response.status} ${response.statusText}`)
    const releases = (await response.json()) as GitHubRelease[]
    const config = (globalThis as unknown as { VITEPRESS_CONFIG: SiteConfig }).VITEPRESS_CONFIG
    const md = await createMarkdownRenderer(config.srcDir, { ...config.markdown, html: false }, config.site.base, config.logger)
    return releases.filter((release) => !release.draft).map((release) => ({
      tag: release.tag_name,
      date: new Date(release.published_at).toLocaleDateString('en-GB', {
        day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC',
      }),
      url: release.html_url,
      notes: md.render((release.body ?? '')
        .replace(/<!--[\s\S]*?-->/g, '')
        .replace(/^#{1,6} (.+)$/gm, '**$1**')),
    }))
  },
})
