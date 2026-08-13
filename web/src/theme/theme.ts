import { useEffect, useState } from 'react'

export type Theme = 'light' | 'dark'

export const DARK_THEME_CLASS = 'pf-v6-theme-dark'
export const THEME_STORAGE_KEY = 'dufflebag-theme'
export const THEME_MEDIA_QUERY = '(prefers-color-scheme: dark)'

export function resolveTheme(stored: string | null, systemDark: boolean): Theme {
  if (stored === 'light' || stored === 'dark') return stored
  return systemDark ? 'dark' : 'light'
}

export function applyTheme(
  theme: Theme,
  root: Pick<HTMLElement, 'classList'> = document.documentElement,
) {
  root.classList.toggle(DARK_THEME_CLASS, theme === 'dark')
}

export function watchSystemTheme(
  media: Pick<MediaQueryList, 'addEventListener' | 'removeEventListener'>,
  stored: () => string | null,
  onTheme: (theme: Theme) => void,
) {
  const onChange = (event: MediaQueryListEvent) => {
    const override = stored()
    if (override === 'light' || override === 'dark') return
    onTheme(resolveTheme(override, event.matches))
  }
  media.addEventListener('change', onChange)
  return () => media.removeEventListener('change', onChange)
}

export function useTheme() {
  const [media] = useState<MediaQueryList>(() => window.matchMedia(THEME_MEDIA_QUERY))
  const [theme, setTheme] = useState<Theme>(() =>
    resolveTheme(window.localStorage.getItem(THEME_STORAGE_KEY), media.matches))

  useEffect(() => applyTheme(theme), [theme])
  useEffect(() => watchSystemTheme(
    media,
    () => window.localStorage.getItem(THEME_STORAGE_KEY),
    (next) => {
      applyTheme(next)
      setTheme(next)
    },
  ), [media])

  return { theme, setTheme }
}
