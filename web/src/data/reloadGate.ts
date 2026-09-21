export type ReloadGate = {
  begin: () => number | null
  request: () => boolean
  settle: (run: number) => boolean
}

/** Coalesces any number of reload requests behind the active load. */
export function createReloadGate(): ReloadGate {
  let active: number | null = null
  let nextRun = 0
  let pending = false
  return {
    begin: () => {
      if (active !== null) return null
      active = ++nextRun
      return active
    },
    request: () => {
      if (active === null) return true
      pending = true
      return false
    },
    settle: (run) => {
      if (active !== run) return false
      active = null
      if (!pending) return false
      pending = false
      return true
    },
  }
}
