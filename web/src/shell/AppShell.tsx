import { forwardRef, type ComponentProps, type ReactNode } from 'react'
import { NavLink, useLocation } from 'react-router'
import {
  Button, Masthead, MastheadBrand, MastheadContent, MastheadLogo, MastheadMain, MastheadToggle,
  Nav, NavGroup, NavItem, Page, PageSidebar, PageSidebarBody,
  PageToggleButton, Toolbar, ToolbarContent, ToolbarItem,
} from '@patternfly/react-core'
import BarsIcon from '@patternfly/react-icons/dist/esm/icons/bars-icon'

import { useAuth } from '../auth/AuthContext'
import { visibleNavItems, type NavKey } from '../auth/permissions'
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

function AppMasthead() {
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
              <SignOutButton signOut={signOut} />
            </ToolbarItem>
          </ToolbarContent>
        </Toolbar>
      </MastheadContent>
    </Masthead>
  )
}

export function SignOutButton({ signOut }: { signOut: (reason: 'requested') => void }) {
  return <Button variant="link" onClick={() => signOut('requested')}>Sign out</Button>
}

export function AppShell({ children }: { children: ReactNode }) {
  const { pathname } = useLocation()
  const { self } = useAuth()

  return (
    <AppShellView
      pathname={pathname}
      visibleItems={visibleNavItems(self?.role ?? null)}
      masthead={<AppMasthead />}
    >
      {children}
    </AppShellView>
  )
}

export function AppShellView({
  children,
  pathname,
  visibleItems,
  masthead,
}: {
  children: ReactNode
  pathname: string
  visibleItems: NavKey[]
  masthead?: ReactNode
}) {
  const visibleGroups = NAV
    .map(({ group, items }) => ({
      group,
      items: items.filter(({ key }) => visibleItems.includes(key)),
    }))
    .filter(({ items }) => items.length > 0)

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
                  isActive={pathname.startsWith(to)}
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
