import { forwardRef, type ComponentProps, type ReactNode } from 'react'
import { NavLink, useLocation } from 'react-router'
import {
  Button, Masthead, MastheadBrand, MastheadContent, MastheadLogo, MastheadMain, MastheadToggle,
  Nav, NavGroup, NavItem, Page, PageSidebar, PageSidebarBody,
  PageToggleButton, Toolbar, ToolbarContent, ToolbarItem,
} from '@patternfly/react-core'
import BarsIcon from '@patternfly/react-icons/dist/esm/icons/bars-icon'
import MoonIcon from '@patternfly/react-icons/dist/esm/icons/moon-icon'
import OutlinedQuestionCircleIcon from '@patternfly/react-icons/dist/esm/icons/outlined-question-circle-icon'
import SunIcon from '@patternfly/react-icons/dist/esm/icons/sun-icon'

import { useAuth } from '../auth/AuthContext'
import { visibleNavItems, type NavKey } from '../auth/permissions'
import { applyTheme, THEME_STORAGE_KEY, type Theme } from '../theme/theme'
import { TenantSwitcher } from './TenantSwitcher'

type NavGroupModel = {
  group: string
  items: readonly { key: NavKey; to: string; label: string }[]
}

/** Nav grouped as the design labels it: Registry, then Administration. */
const NAV: readonly NavGroupModel[] = [
  { group: 'Registry', items: [
    { key: 'buckets', to: '/buckets', label: 'Buckets' },
  ]},
  // Instance stays under Administration where the design put it: it is
  // reader-tier, so the group renders for every role — role filtering changes
  // WHICH administration items appear, never the shipped grouping.
  { group: 'Administration', items: [
    { key: 'principals', to: '/principals', label: 'Principals' },
    { key: 'audit', to: '/audit', label: 'Audit' },
    { key: 'encryption', to: '/encryption', label: 'Encryption' },
    { key: 'bagdrop', to: '/bagdrop', label: 'Bag Drop' },
    { key: 'webhooks', to: '/webhooks', label: 'Webhooks' },
    { key: 'instance', to: '/instance', label: 'Instance' },
  ]},
]

type RouterNavLinkProps = Omit<ComponentProps<typeof NavLink>, 'to'> & { href?: string }

// PatternFly's component slot supplies link destinations as href; the router
// consumes the same destination as to.
const RouterNavLink = forwardRef<HTMLAnchorElement, RouterNavLinkProps>(
  ({ href = '', ...props }, ref) => <NavLink ref={ref} to={href} {...props} />,
)

function AppMasthead({
  theme,
  onThemeChange,
}: {
  theme: Theme
  onThemeChange: (theme: Theme) => void
}) {
  const { signOut } = useAuth()

  return (
    <Masthead>
      <MastheadMain>
        <MastheadToggle>
          <PageToggleButton variant="plain" aria-label="Global navigation">
            <BarsIcon />
          </PageToggleButton>
        </MastheadToggle>
        <MastheadBrand>
          <MastheadLogo className="app-wordmark">
            {/* The wordmark is drawn letterforms on the mark's own grid - one
                SVG so the shared baseline is structural (BRAND.md par.8). The
                seams carry their own stroke: they are the only Packer blue. */}
            {/* The viewBox crops to the true ink bounds - path extremes
                (y 6.5..46.5) plus half the stroke and its round caps - so
                flex-centering aligns the lockup's visual mass with the
                hamburger and picker centres without clipping a stroke. */}
            <svg
              className="db-lockup--single"
              width="144"
              height="24.9"
              viewBox="0 5.2 246 42.6"
              fill="none"
              stroke="currentColor"
              strokeWidth="3"
              strokeLinecap="round"
              strokeLinejoin="round"
              role="img"
              aria-label="dufflebag"
            >
              <rect x="5" y="18" width="38" height="22" rx="8" />
              <path d="M16.5 18C16.5 9.5 20 6.5 24 6.5C28 6.5 31.5 9.5 31.5 18" />
              <path className="db-lockup__seams" d="M17 18.5V39.5M31 18.5V39.5" stroke="var(--db-seam)" />
              <g transform="translate(57,0)">
                <ellipse cx="13" cy="29" rx="10" ry="11" />
                <path d="M23 6.5V40" />
                <path d="M27 18V30C27 36 31.5 40 37 40C42.5 40 47 36 47 30V18" />
                <path d="M57 40V12C57 8.5 59.5 6.5 63 6.5" />
                <path d="M71 40V12C71 8.5 73.5 6.5 77 6.5" />
                <path d="M52 18H78" />
                <path d="M82 6.5V40" />
                <path d="M88 29H108" />
                <path d="M108 29V26.5C108 21 103.5 18 98 18C92.5 18 88 21.5 88 27V31C88 36.5 92.5 40 98 40C102 40 105.5 38 107.5 35" />
                <path d="M112 6.5V40" />
                <ellipse cx="122" cy="29" rx="10" ry="11" />
                <ellipse cx="146" cy="29" rx="10" ry="11" />
                <path d="M156 18V40" />
                <ellipse cx="170" cy="29" rx="10" ry="11" />
                <path d="M180 18V39C180 44 176 46.5 171 46.5C167 46.5 164 45.5 162 43.5" />
              </g>
            </svg>
          </MastheadLogo>
        </MastheadBrand>
      </MastheadMain>
      <MastheadContent>
        <Toolbar isStatic isFullHeight>
          <ToolbarContent>
            <ToolbarItem className="tenant-switcher-item">
              <TenantSwitcher />
            </ToolbarItem>
            <ToolbarItem align={{ default: 'alignEnd' }}>
              <ThemeToggleButton theme={theme} onThemeChange={onThemeChange} />
            </ToolbarItem>
            <ToolbarItem>
              <Button
                component="a"
                variant="plain"
                href="https://benemon.github.io/dufflebag"
                target="_blank"
                rel="noreferrer"
                aria-label="Documentation"
              >
                <OutlinedQuestionCircleIcon />
              </Button>
            </ToolbarItem>
            <ToolbarItem>
              <SignOutButton signOut={signOut} />
            </ToolbarItem>
          </ToolbarContent>
        </Toolbar>
      </MastheadContent>
    </Masthead>
  )
}

export function ThemeToggleButton({
  theme,
  onThemeChange,
  storage = window.localStorage,
  root = document.documentElement,
}: {
  theme: Theme
  onThemeChange: (theme: Theme) => void
  storage?: Pick<Storage, 'setItem'>
  root?: Pick<HTMLElement, 'classList'>
}) {
  const next = theme === 'light' ? 'dark' : 'light'
  return (
    <Button
      variant="plain"
      aria-label={`Switch to ${next} theme`}
      onClick={() => {
        storage.setItem(THEME_STORAGE_KEY, next)
        applyTheme(next, root)
        onThemeChange(next)
      }}
    >
      {theme === 'light' ? <MoonIcon /> : <SunIcon />}
    </Button>
  )
}

export function SignOutButton({ signOut }: { signOut: (reason: 'requested') => void }) {
  return <Button variant="link" onClick={() => signOut('requested')}>Sign out</Button>
}

export function AppShell({
  children,
  theme,
  onThemeChange,
}: {
  children: ReactNode
  theme: Theme
  onThemeChange: (theme: Theme) => void
}) {
  const { pathname } = useLocation()
  const { self, state, selectedBucket } = useAuth()
  // A bucket-scoped session has exactly one bucket and lands in it; a Buckets
  // entry would offer a list of one. It still needs a way BACK — admin screens
  // otherwise strand it (duf-xmg5) — so the entry becomes 'Bucket', pointing
  // at the bucket itself. The claim carries only the bucket's id; the carried
  // selection names it when it matches, and the landing route re-resolves the
  // claim when it does not.
  const bucketScoped = state?.claims.bucketID != null
  const bucketNav = bucketScoped
    ? {
      to: selectedBucket && selectedBucket.id === state?.claims.bucketID
        ? `/buckets/${encodeURIComponent(selectedBucket.name)}`
        : '/',
    }
    : null

  return (
    <AppShellView
      pathname={pathname}
      visibleItems={visibleNavItems(self?.role ?? null)}
      bucketNav={bucketNav}
      masthead={<AppMasthead theme={theme} onThemeChange={onThemeChange} />}
    >
      {children}
    </AppShellView>
  )
}

export function AppShellView({
  children,
  pathname,
  visibleItems,
  bucketNav = null,
  masthead,
}: {
  children: ReactNode
  pathname: string
  visibleItems: NavKey[]
  /** Bucket-scoped sessions: swap Buckets for a 'Bucket' entry at this target. */
  bucketNav?: { to: string } | null
  masthead?: ReactNode
}) {
  const visibleGroups = NAV
    .map(({ group, items }) => ({
      group,
      items: items
        .filter(({ key }) => visibleItems.includes(key))
        .map((item) => (item.key === 'buckets' && bucketNav
          ? { ...item, to: bucketNav.to, label: 'Bucket' }
          : item)),
    }))
    .filter(({ items }) => items.length > 0)

  // The swapped entry means "your bucket": current on the landing (mid-
  // resolution) and on the bucket's own routes, bounded at path segments so a
  // sibling bucket route — reachable by URL, refused by the server — does not
  // light it up, and neither does a name that happens to share a prefix.
  const isActive = (key: NavKey, to: string) => {
    if (key === 'buckets' && bucketNav) {
      if (pathname === '/') return true
      return to !== '/' && (pathname === to || pathname.startsWith(`${to}/`))
    }
    return pathname.startsWith(to)
  }

  const sidebar = (
    <PageSidebar className="app-sidebar">
      <PageSidebarBody>
        <Nav className="app-global-nav" aria-label="Global">
          {visibleGroups.map(({ group, items }) => (
            <NavGroup key={group} title={group}>
              {items.map(({ key, to, label }) => (
                <NavItem
                  key={key}
                  component={RouterNavLink}
                  to={to}
                  isActive={isActive(key, to)}
                >
                  {label}
                </NavItem>
              ))}
            </NavGroup>
          ))}
        </Nav>
      </PageSidebarBody>
    </PageSidebar>
  )

  return (
    <Page
      className="app-page"
      masthead={masthead}
      sidebar={sidebar}
      isManagedSidebar
      isContentFilled
    >
      {children}
    </Page>
  )
}
