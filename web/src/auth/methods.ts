import { useEffect, useState } from 'react'

/**
 * Which human sign-in methods this instance offers.
 *
 * Read unauthenticated, because the login page must render before anyone has
 * signed in. That discloses only what the page itself shows, and nothing about
 * whether any particular account exists.
 *
 * Service principals are deliberately absent from this list: they are always
 * available and cannot be turned off, which is what stops a misconfigured
 * identity provider locking an operator out of their own instance (ADR-0019).
 */
export type HumanMethods = {
  basic: boolean
  oidc: { enabled: boolean; label: string } | null
}

const NONE: HumanMethods = { basic: false, oidc: null }

export function useHumanMethods(): { methods: HumanMethods; loading: boolean } {
  const [methods, setMethods] = useState<HumanMethods>(NONE)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let cancelled = false
    void fetch('/auth/methods')
      .then((response) => (response.ok ? response.json() : NONE))
      .then((body: unknown) => {
        if (cancelled) return
        const parsed = body as Partial<HumanMethods>
        setMethods({
          basic: parsed.basic === true,
          oidc: parsed.oidc?.enabled ? { enabled: true, label: parsed.oidc.label || 'your provider' } : null,
        })
      })
      // The endpoint does not exist yet. Falling back to none is the truthful
      // answer today and the safe one in general: offering a method that is not
      // configured would produce a sign-in that cannot succeed.
      .catch(() => {
        if (!cancelled) setMethods(NONE)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [])

  return { methods, loading }
}
