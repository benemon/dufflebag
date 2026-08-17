import {
  createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode,
} from 'react'

import {
  clearSession, fetchSession, getOrganization, getOrganizationProject,
  getSelf, listOrganizationProjects, listOrganizations, listProjects,
  requestToken, signOutIfUnauthorized, storeSession,
  type ApiOrganization, type ApiProject, type ApiSelf,
} from '../api/client'
import { decodeClaims, isExpired, type TokenClaims } from './token'

/**
 * Authentication for the console.
 *
 * The UI authenticates through the same token endpoint as the CLI and carries
 * the same JWT, so there is one authorization model rather than two (ADR-0006).
 *
 * THE TOKEN NEVER TOUCHES WEB STORAGE. Not localStorage, not sessionStorage: a
 * bearer token in web storage is readable by any script that gets injected. It
 * is held in memory, and a reload gets it back from the server's httpOnly
 * session cookie (/sys/session, duf-1cn) — which script cannot read either.
 * The cookie session renews its bearer token shortly before expiry while the
 * originating secret remains active, bounded by an eight-hour absolute cap.
 *
 * # A platform-scoped session chooses its tenancy
 *
 * A token with no organization claim is the bootstrap root (duf-tkw): it sits
 * above every tenancy rather than inside one, so the session carries a chosen
 * organisation alongside the chosen project. For a tenancy-scoped session the
 * organisation is the token's and is never a choice.
 */
type AuthState = {
  token: string
  claims: TokenClaims
}

type OrganizationRefreshState = {
  organizations: ApiOrganization[]
  failure: string | null
}

type OrganizationRefreshResult =
  | { kind: 'listed'; organizations: ApiOrganization[] }
  | { kind: 'failed'; failure: string }

export function applyOrganizationRefresh(
  current: OrganizationRefreshState,
  result: OrganizationRefreshResult,
): OrganizationRefreshState {
  return result.kind === 'listed'
    ? { organizations: result.organizations, failure: null }
    : { organizations: current.organizations, failure: result.failure }
}

/**
 * The empty string is organisation standing — the deliberate step above
 * projects — and must survive a refresh exactly as it survives the oldest-
 * project auto-select ('' is not nullish). A refresh triggered by opening the
 * picker once raced the dash selection and stomped it to the oldest project.
 */
export function selectionAfterProjectsRefresh(
  current: string | null,
  ordered: ApiProject[],
): string | null {
  if (current === '') return current
  if (current && ordered.some((project) => project.id === current)) return current
  return ordered[0]?.id ?? null
}

export function selectionAfterOrganizationRefresh(
  current: string | null,
  organizations: ApiOrganization[],
): string | null {
  if (current && !organizations.some((organization) => organization.id === current)) return ''
  return current
}

export function selectionAfterInitialOrganizationLoad(
  current: string | null,
  organizations: ApiOrganization[],
): string | null {
  return current ?? (organizations.length === 1 ? (organizations[0]?.id ?? null) : null)
}

type OrganizationRefreshFlight<T> = { current: Promise<T> | null }

export function startOrganizationRefresh<T>(
  flight: OrganizationRefreshFlight<T>,
  start: () => Promise<T>,
): Promise<T> {
  if (flight.current) return flight.current
  const pending = start().finally(() => {
    if (flight.current === pending) flight.current = null
  })
  flight.current = pending
  return pending
}

type AuthContextValue = {
  state: AuthState | null
  /** The current principal as resolved from storage by GET /api/v1/self. */
  self: ApiSelf | null
  selfLoading: boolean
  selfFailure: string | null
  /** True while the boot exchange asks whether a session survives the reload. */
  restoring: boolean
  /**
   * True only when the session ended UNDER the operator — a 401 bounced them to
   * sign-in. An explicit sign-out and a fresh arrival both leave it false: the
   * notice this drives belongs to the transition, not the screen (duf-1cn).
   */
  sessionEnded: boolean
  signIn: (clientID: string, clientSecret: string) => Promise<void>
  /** The reason is required so every call site says which transition this is. */
  signOut: (reason: 'expired' | 'requested') => void
  /**
   * Organisations a platform-scoped session may choose between. Always empty
   * for a tenancy-scoped session, whose organisation is not a choice.
   */
  organizations: ApiOrganization[]
  /** Name of the fixed organization binding for a tenancy-scoped session. */
  boundOrganizationName: string | null
  organizationsLoading: boolean
  organizationFailure: string | null
  organizationRefreshFailure: string | null
  refreshOrganizations: () => Promise<ApiOrganization[] | null>
  /**
   * The organisation in effect: chosen for a platform session, the token's
   * otherwise. '' is the platform row — a platform session that deliberately
   * stepped back up to platform standing (ADR-0014's "nothing selected").
   */
  selectedOrganization: string | null
  selectOrganization: (organizationID: string) => void
  /** Tenants this token actually permits — never a free-text choice. */
  permittedProjects: string[]
  /** Display names for permitted projects, keyed by id. Presentation only. */
  projectNames: Record<string, string>
  selectedProject: string | null
  selectProject: (project: string) => void
  /**
   * The bucket in effect, carried across screens like the rest of the
   * tenancy: set when a bucket route is visited or the picker selects one,
   * cleared when the organisation or project changes. Bucket routes remain
   * authoritative — visiting one re-syncs this. Holds both names: the id is
   * what scope claims and principal creation speak, the name is what routes
   * and the compat plane speak.
   */
  selectedBucket: { id: string; name: string } | null
  selectBucket: (bucket: { id: string; name: string } | null) => void
  projectsLoading: boolean
  projectFailure: string | null
  refreshProjects: () => Promise<ApiProject[] | null>
}

// Exported for the unit tests, which render context consumers under a
// constructed session; the app itself always goes through AuthProvider/useAuth.
export const AuthContext = createContext<AuthContextValue | null>(null)

type RenewalScheduler = (
  callback: () => void, delay: number,
) => ReturnType<typeof setTimeout>
type RenewalClearer = (timer: ReturnType<typeof setTimeout>) => void

export function scheduleSessionRenewal(
  claims: TokenClaims,
  renew: () => void,
  schedule: RenewalScheduler = setTimeout,
  clear: RenewalClearer = clearTimeout,
): (() => void) | null {
  const delay = claims.expiresAt.getTime() - Date.now() - 60_000
  if (delay <= 0) return null
  const timer = schedule(renew, delay)
  return () => clear(timer)
}

export async function renewConsoleSession(
  read: () => Promise<string | null>,
  enter: (token: string, claims: TokenClaims) => void,
  expire: () => void,
): Promise<void> {
  const token = await read()
  const claims = token ? decodeClaims(token) : null
  if (!token || !claims || isExpired(claims)) {
    expire()
    return
  }
  enter(token, claims)
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<AuthState | null>(null)
  const [self, setSelf] = useState<ApiSelf | null>(null)
  const [selfLoading, setSelfLoading] = useState(false)
  const [selfFailure, setSelfFailure] = useState<string | null>(null)
  const [organizationRefresh, setOrganizationRefresh] = useState<OrganizationRefreshState>({
    organizations: [],
    failure: null,
  })
  const organizations = organizationRefresh.organizations
  const [boundOrganizationName, setBoundOrganizationName] = useState<string | null>(null)
  const [organizationsLoading, setOrganizationsLoading] = useState(false)
  const [organizationFailure, setOrganizationFailure] = useState<string | null>(null)
  const [selectedOrganization, setSelectedOrganization] = useState<string | null>(null)
  const [selectedProject, setSelectedProject] = useState<string | null>(null)
  const [organizationProjects, setOrganizationProjects] = useState<ApiProject[]>([])
  const [projectsLoading, setProjectsLoading] = useState(false)
  const [projectFailure, setProjectFailure] = useState<string | null>(null)

  const [restoring, setRestoring] = useState(true)
  const [sessionEnded, setSessionEnded] = useState(false)
  const organizationSession = useRef(0)
  const organizationRefreshFlight = useRef<Promise<ApiOrganization[] | null> | null>(null)

  const enterSession = useCallback((token: string, claims: TokenClaims) => {
    organizationSession.current += 1
    organizationRefreshFlight.current = null
    setState({ token, claims })
    setSelf(null)
    setSelfLoading(true)
    setSelfFailure(null)
    setSessionEnded(false)
    setOrganizationRefresh({ organizations: [], failure: null })
    setBoundOrganizationName(null)
    setOrganizationsLoading(claims.organizationID === null)
    setOrganizationFailure(null)
    setSelectedOrganization(claims.organizationID)
    setSelectedProject(claims.projectID)
    setOrganizationProjects([])
    setProjectsLoading(claims.organizationID !== null && !claims.projectID)
    setProjectFailure(null)
  }, [])

  const signIn = useCallback(async (clientID: string, clientSecret: string) => {
    const token = await requestToken(clientID, clientSecret)
    const claims = decodeClaims(token)
    if (!claims || isExpired(claims)) {
      throw new Error('The server returned a token that is unusable.')
    }
    enterSession(token, claims)
    // Fire-and-forget: the cookie only spares the next reload a sign-in, so a
    // failure to set it must not fail the sign-in that just succeeded.
    void storeSession(token).catch(() => {})
  }, [enterSession])

  // The boot exchange: ask the server whether a session survives this reload.
  // Ends in one of the three arrivals sign-in distinguishes — resumed (no
  // sign-in screen at all), or fresh (screen, no notice). The third, expired
  // mid-use, is signOut('expired')'s to mark.
  useEffect(() => {
    let cancelled = false
    void fetchSession()
      .then((token) => {
        if (cancelled || !token) return
        const claims = decodeClaims(token)
        if (claims && !isExpired(claims)) enterSession(token, claims)
      })
      .catch(() => {})
      .finally(() => {
        if (!cancelled) setRestoring(false)
      })
    return () => {
      cancelled = true
    }
  }, [enterSession])

  const signOut = useCallback((reason: 'expired' | 'requested') => {
    organizationSession.current += 1
    organizationRefreshFlight.current = null
    setState(null)
    setSelf(null)
    setSelfLoading(false)
    setSelfFailure(null)
    setSessionEnded(reason === 'expired')
    void clearSession().catch(() => {})
    setOrganizationRefresh({ organizations: [], failure: null })
    setBoundOrganizationName(null)
    setOrganizationsLoading(false)
    setOrganizationFailure(null)
    setSelectedOrganization(null)
    setSelectedProject(null)
    setSelectedBucket(null)
    setOrganizationProjects([])
    setProjectsLoading(false)
    setProjectFailure(null)
  }, [])

  useEffect(() => {
    if (!state) return
    const cancel = scheduleSessionRenewal(state.claims, () => {
      void renewConsoleSession(fetchSession, enterSession, () => signOut('expired')).catch(() => {})
    })
    return cancel ?? undefined
  }, [state, enterSession, signOut])

  // Role is deliberately absent from the token. Read the principal the same
  // way the server does, so console affordances follow the stored role.
  useEffect(() => {
    if (!state) return
    let cancelled = false
    setSelfLoading(true)
    setSelfFailure(null)
    void getSelf(state.token)
      .then((loaded) => {
        if (!cancelled) setSelf(loaded)
      })
      .catch((err: unknown) => {
        if (cancelled || signOutIfUnauthorized(err, signOut)) return
        setSelf(null)
        setSelfFailure(err instanceof Error ? err.message : 'Could not load your role.')
      })
      .finally(() => {
        if (!cancelled) setSelfLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [state, signOut])

  // Switching organisation drops the project selection: carrying one across
  // would scope queries to a project the new organisation does not contain.
  // '' — the platform row — is the deliberate step back up to platform standing
  // (ADR-0014), so it clears the pair the same way and lists nothing.
  const [selectedBucket, setSelectedBucket] = useState<{ id: string; name: string } | null>(null)
  const projectEpoch = useRef(0)
  const selectOrganization = useCallback((organizationID: string) => {
    // A refresh in flight for the previous organisation must not land its
    // projects onto this one; bumping the epoch tells it to discard.
    projectEpoch.current += 1
    setSelectedOrganization(organizationID)
    setSelectedProject(null)
    // A bucket belongs to the pair that named it.
    setSelectedBucket(null)
    setOrganizationProjects([])
    // For a real organisation, marked loading NOW rather than when the effect
    // runs, so no render in between can mistake "not listed yet" for "listed
    // and empty". At platform standing there is nothing to list, so nothing
    // is loading.
    setProjectsLoading(organizationID !== '')
    setProjectFailure(null)
  }, [])

  // A project-scoped token permits exactly one project; an organization-scoped
  // one permits several, and the list comes from the server rather than from
  // anything the user can type. Letting the UI name a tenant would reintroduce,
  // in the client, the flaw ADR-0017 closes on the server.
  const permittedProjects = useMemo(() => {
    if (!state) return []
    return state.claims.projectID
      ? [state.claims.projectID]
      : organizationProjects.map((project) => project.id)
  }, [state, organizationProjects])

  const projectNames = useMemo(
    () => Object.fromEntries(organizationProjects.map((project) => [project.id, project.name])),
    [organizationProjects],
  )

  // A tenancy-scoped session cannot choose another organization, but it may
  // read the name of the one already carried in its token.
  useEffect(() => {
    if (!state || state.claims.organizationID === null) {
      setBoundOrganizationName(null)
      return
    }
    let cancelled = false
    void getOrganization(state.token, state.claims.organizationID)
      .then((organization) => {
        if (!cancelled) setBoundOrganizationName(organization.name)
      })
      .catch((err: unknown) => {
        if (!cancelled) signOutIfUnauthorized(err, signOut)
      })
    return () => {
      cancelled = true
    }
  }, [state, signOut])

  // A platform session discovers which organisations exist before anything can
  // be scoped. Exactly one is selected automatically — the contract the
  // unpinned CLI applies ("exactly one or it errors", ADR-0003) — while several
  // stay unselected until the operator chooses, and zero stays honestly empty.
  // The auto-select only ever fills a null: a deliberate '' — the platform row,
  // platform standing — is not nullish, so it is never silently undone.
  useEffect(() => {
    if (!state || state.claims.organizationID !== null) {
      setOrganizationsLoading(false)
      setOrganizationFailure(null)
      return
    }
    let cancelled = false
    setOrganizationsLoading(true)
    setOrganizationFailure(null)
    void listOrganizations(state.token)
      .then((listed) => {
        if (cancelled) return
        setOrganizationRefresh({ organizations: listed, failure: null })
        setSelectedOrganization((current) => selectionAfterInitialOrganizationLoad(current, listed))
        // The auto-selection above means projects are about to load; saying so
        // now keeps "listed and empty" unclaimable until it is true.
        if (listed.length === 1) setProjectsLoading(true)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        if (signOutIfUnauthorized(err, signOut)) return
        setOrganizationRefresh({ organizations: [], failure: null })
        setOrganizationFailure(err instanceof Error ? err.message : 'Could not load organisations.')
      })
      .finally(() => {
        if (!cancelled) setOrganizationsLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [state, signOut])

  // Opening the platform picker refreshes what is already known without
  // turning a good list back into an initial-loading state. Refresh success
  // never auto-selects. A deleted current organisation is the one exception:
  // its absence is authoritative, so the selection steps back to platform.
  const refreshOrganizations = useCallback(() => {
    if (!state || state.claims.organizationID !== null) return Promise.resolve(null)
    return startOrganizationRefresh(organizationRefreshFlight, async () => {
      const session = organizationSession.current
      setOrganizationRefresh((current) => ({ ...current, failure: null }))
      try {
        const listed = await listOrganizations(state.token)
        if (session !== organizationSession.current) return null
        setOrganizationRefresh((current) => applyOrganizationRefresh(current, {
          kind: 'listed', organizations: listed,
        }))
        setSelectedOrganization((current) => selectionAfterOrganizationRefresh(current, listed))
        return listed
      } catch (err: unknown) {
        if (session !== organizationSession.current) return null
        if (signOutIfUnauthorized(err, signOut)) return null
        setOrganizationRefresh((current) => applyOrganizationRefresh(current, {
          kind: 'failed',
          failure: err instanceof Error ? err.message : 'Could not refresh organisations.',
        }))
        return null
      }
    })
  }, [state, signOut])

  useEffect(() => {
    if (!state) {
      setProjectsLoading(false)
      setProjectFailure(null)
      return
    }
    if (state.claims.projectID) {
      let cancelled = false
      setProjectsLoading(true)
      setProjectFailure(null)
      void getOrganizationProject(
        state.token,
        state.claims.organizationID as string,
        state.claims.projectID,
      )
        .then((project) => {
          if (!cancelled) setOrganizationProjects([project])
        })
        .catch((err: unknown) => {
          if (cancelled) return
          if (signOutIfUnauthorized(err, signOut)) return
          setOrganizationProjects([])
          setProjectFailure(err instanceof Error ? err.message : 'Could not load project.')
        })
        .finally(() => {
          if (!cancelled) setProjectsLoading(false)
        })
      return () => {
        cancelled = true
      }
    }
    const platform = state.claims.organizationID === null
    if (platform && !selectedOrganization) {
      // Nothing to list until an organisation is chosen; fabricating one here
      // is exactly what this flow exists to avoid.
      setOrganizationProjects([])
      setSelectedProject(null)
      setProjectsLoading(false)
      setProjectFailure(null)
      return
    }
    let cancelled = false
    setProjectsLoading(true)
    setProjectFailure(null)
    // A tenancy session asks the resource-manager plane, the same way the CLI
    // does (ADR-0006). A platform session cannot — that listing deliberately
    // refuses a caller with no organisation of its own (ADR-0016) — so it asks
    // the platform plane about the organisation it chose.
    const listed = platform
      ? listOrganizationProjects(state.token, selectedOrganization as string)
      : listProjects(state.token, state.claims.organizationID as string)
    void listed
      .then((projects) => {
        if (cancelled) return
        // Oldest first, matching how an unpinned CLI chooses, so the console and
        // the CLI default to the same project (ADR-0003).
        const ordered = [...projects].sort((a, b) => a.created_at.localeCompare(b.created_at))
        setOrganizationProjects(ordered)
        setSelectedProject((current) => current ?? ordered[0]?.id ?? null)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        if (signOutIfUnauthorized(err, signOut)) return
        setOrganizationProjects([])
        setSelectedProject(null)
        setProjectFailure(err instanceof Error ? err.message : 'Could not load projects.')
      })
      .finally(() => {
        if (!cancelled) setProjectsLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [state, selectedOrganization, signOut])

  const refreshProjects = useCallback(async (): Promise<ApiProject[] | null> => {
    if (!state) return null
    const platform = state.claims.organizationID === null
    const organizationID = platform ? selectedOrganization : state.claims.organizationID
    if (!organizationID) return null
    const epoch = projectEpoch.current
    setProjectFailure(null)
    try {
      const projects = state.claims.projectID
        ? [await getOrganizationProject(state.token, organizationID, state.claims.projectID)]
        : platform
          ? await listOrganizationProjects(state.token, organizationID)
          : await listProjects(state.token, organizationID)
      if (epoch !== projectEpoch.current) return null
      const ordered = [...projects].sort((a, b) => a.created_at.localeCompare(b.created_at))
      setOrganizationProjects(ordered)
      setSelectedProject((current) => selectionAfterProjectsRefresh(current, ordered))
      return ordered
    } catch (err: unknown) {
      if (epoch !== projectEpoch.current) return null
      if (signOutIfUnauthorized(err, signOut)) return null
      setProjectFailure(err instanceof Error ? err.message : 'Could not refresh projects.')
      return null
    }
  }, [state, selectedOrganization, signOut])

  const value = useMemo(
    () => ({
      state,
      self,
      selfLoading,
      selfFailure,
      restoring,
      sessionEnded,
      signIn,
      signOut,
      organizations,
      boundOrganizationName,
      organizationsLoading,
      organizationFailure,
      organizationRefreshFailure: organizationRefresh.failure,
      refreshOrganizations,
      selectedOrganization,
      selectOrganization,
      permittedProjects,
      projectNames,
      selectedProject,
      selectProject: (project: string) => {
        setSelectedProject(project)
        setSelectedBucket(null)
      },
      selectedBucket,
      selectBucket: setSelectedBucket,
      projectsLoading,
      projectFailure,
      refreshProjects,
    }),
    [
      state, self, selfLoading, selfFailure, restoring, sessionEnded, signIn, signOut,
      organizations, boundOrganizationName, organizationsLoading, organizationFailure,
      organizationRefresh.failure, refreshOrganizations,
      selectedOrganization, selectOrganization,
      permittedProjects, projectNames, selectedProject, selectedBucket,
      projectsLoading, projectFailure, refreshProjects,
    ],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth() {
  const value = useContext(AuthContext)
  if (!value) throw new Error('useAuth used outside AuthProvider')
  return value
}
