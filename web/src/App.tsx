import { useEffect, useState } from 'react'
import { Navigate, Outlet, Route, Routes } from 'react-router'
import {
  Alert, Content, EmptyState, EmptyStateActions, EmptyStateBody, EmptyStateFooter, PageSection,
  Spinner,
} from '@patternfly/react-core'

import { listBuckets, type Tenant } from './api/client'
import { AuthProvider, useAuth } from './auth/AuthContext'
import { AppShell } from './shell/AppShell'
import { Buckets } from './screens/Buckets'
import { Versions } from './screens/Versions'
import { Version } from './screens/Version'
import { Build } from './screens/Build'
import { Login } from './screens/Login'
import { Instance } from './screens/Instance'
import { Principals } from './screens/Principals'
import { Audit } from './screens/Audit'
import { Encryption } from './screens/Encryption'
import { BagDrop } from './screens/BagDrop'
import { Webhooks } from './screens/Webhooks'
import { CreateTenancyButton } from './components/TenancyCreation'
import type { Role } from './auth/permissions'
import { useTheme, type Theme } from './theme/theme'

export function App() {
  const { theme, setTheme } = useTheme()
  return (
    <AuthProvider>
      <Authenticated theme={theme} onThemeChange={setTheme} />
    </AuthProvider>
  )
}

/**
 * Nothing renders until there is a token.
 *
 * The gate is here rather than per-route so a screen added later is denied by
 * default — an unauthenticated route has to be written deliberately, it cannot
 * be arrived at by forgetting something (ADR-0017).
 */
function Authenticated({
  theme,
  onThemeChange,
}: {
  theme: Theme
  onThemeChange: (theme: Theme) => void
}) {
  const {
    state, self, restoring, selectedOrganization, selectedProject, projectsLoading, projectFailure,
  } = useAuth()
  // While the boot exchange asks whether a session survived the reload,
  // showing the sign-in screen would flash a state that may be about to be
  // untrue. Hold the page on an explicit state until the answer is in — it is
  // one same-origin round trip.
  if (!state && restoring) {
    return (
      <PageSection>
        <Spinner aria-label="Restoring your session…" />
        <Content component="p">Restoring your session…</Content>
      </PageSection>
    )
  }
  if (!state) return <Login />
  // A platform-scoped session (no organization claim — the bootstrap root,
  // duf-tkw) is gated on nothing: Principals and Instance need no tenancy, and
  // the data screens explain what is missing until the header picker names one.
  // A tenancy-scoped session keeps its gates, because for it a missing project
  // is a settled fact about the token rather than a choice not yet made.
  const platform = state.claims.organizationID === null
  if (!platform && projectsLoading) {
    return (
      <AppShell theme={theme} onThemeChange={onThemeChange}>
        <PageSection>
          <Spinner aria-label="Loading projects…" />
          <Content component="p">Loading projects…</Content>
        </PageSection>
      </AppShell>
    )
  }
  if (!platform && projectFailure) {
    return (
      <AppShell theme={theme} onThemeChange={onThemeChange}>
        <ProjectLoadFailure failure={projectFailure} />
      </AppShell>
    )
  }
  if (!platform && !selectedProject) {
    return (
      <AppShell theme={theme} onThemeChange={onThemeChange}>
        <PageSection>
          <NoProjectsYet
            callerRole={self?.role ?? null}
            organizationID={selectedOrganization ?? undefined}
          />
        </PageSection>
      </AppShell>
    )
  }

  return (
    <Routes>
      <Route element={<ShellRoute theme={theme} onThemeChange={onThemeChange} />}>
        <Route path="/" element={<Landing />} />
        <Route path="/buckets" element={<Buckets />} />
        <Route path="/principals" element={<Principals />} />
        <Route path="/audit" element={<Audit />} />
        <Route path="/encryption" element={<Encryption />} />
        <Route path="/bagdrop" element={<BagDrop />} />
        <Route path="/webhooks" element={<Webhooks />} />
        <Route path="/instance" element={<Instance />} />
      </Route>
      <Route
        path="/buckets/:bucket"
        element={<ShellRoute theme={theme} onThemeChange={onThemeChange} />}
      >
        <Route index element={<Versions />} />
        <Route path="versions/:fingerprint" element={<Version />} />
        <Route path="versions/:fingerprint/builds/:build" element={<Build />} />
      </Route>
    </Routes>
  )
}

// Above-bucket sessions land on the Buckets screen; a bucket-scoped session
// has exactly one bucket and lands in it — a one-row list would be noise, and
// the masthead picker already says where it landed. The claim carries the
// bucket's id; the scoped listing (which contains exactly that bucket) names
// it for the route.
function Landing() {
  const { state } = useAuth()
  const claims = state?.claims
  if (!claims?.bucketID || !claims.organizationID || !claims.projectID) {
    return <Navigate to="/buckets" replace />
  }
  return (
    <ScopedBucketLanding
      token={state!.token}
      tenant={{ organizationID: claims.organizationID, projectID: claims.projectID }}
      bucketID={claims.bucketID}
    />
  )
}

function ScopedBucketLanding({ token, tenant, bucketID }: {
  token: string
  tenant: Tenant
  bucketID: string
}) {
  const [name, setName] = useState<string | null>(null)
  const [failure, setFailure] = useState<string | null>(null)
  useEffect(() => {
    let cancelled = false
    listBuckets(token, tenant)
      .then((buckets) => {
        if (cancelled) return
        const scoped = buckets.find((bucket) => bucket.id === bucketID)
        if (scoped) setName(scoped.name)
        else setFailure('The session names a bucket the listing cannot see.')
      })
      .catch((err: unknown) => {
        if (!cancelled) setFailure(err instanceof Error ? err.message : 'Could not resolve the bucket.')
      })
    return () => {
      cancelled = true
    }
  }, [token, tenant.organizationID, tenant.projectID, bucketID])
  if (failure) {
    return (
      <PageSection>
        <Content component="p">{failure}</Content>
      </PageSection>
    )
  }
  if (name === null) {
    return (
      <PageSection>
        <Spinner aria-label="Resolving your bucket…" />
      </PageSection>
    )
  }
  return <Navigate to={`/buckets/${encodeURIComponent(name)}`} replace />
}

function ShellRoute({
  theme, onThemeChange,
}: {
  theme: Theme
  onThemeChange: (theme: Theme) => void
}) {
  return (
    <AppShell theme={theme} onThemeChange={onThemeChange}>
      <Outlet />
    </AppShell>
  )
}

export function NoProjectsYet({
  callerRole, organizationID,
}: {
  callerRole: Role | null
  organizationID?: string
}) {
  return (
    <EmptyState titleText="No projects yet" headingLevel="h2">
      <EmptyStateBody>A project scopes buckets, principals and channels.</EmptyStateBody>
      <EmptyStateFooter>
        <EmptyStateActions>
          <CreateTenancyButton
            kind="project"
            callerRole={callerRole}
            organizationID={organizationID}
          />
        </EmptyStateActions>
      </EmptyStateFooter>
    </EmptyState>
  )
}

export function ProjectLoadFailure({ failure }: { failure: string }) {
  return (
    <PageSection>
      <Alert variant="danger" isInline title="Projects could not be loaded">
        <Content component="p">{failure}</Content>
      </Alert>
    </PageSection>
  )
}
