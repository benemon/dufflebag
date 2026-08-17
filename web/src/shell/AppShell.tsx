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
          <MastheadLogo className="app-wordmark">dufflebag</MastheadLogo>
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
