import { useEffect, useRef } from 'react'

export function refreshDelay(hot: boolean, visible: boolean): number | null {
  if (!visible) return null
  return hot ? 5_000 : 30_000
}

export function useAutoRefresh({
  hot,
  onRefresh,
}: {
  hot: boolean
  onRefresh: () => void
}): void {
  const refresh = useRef(onRefresh)
  useEffect(() => {
    refresh.current = onRefresh
  }, [onRefresh])

  useEffect(() => {
    let timer: ReturnType<typeof window.setInterval> | undefined

    const restart = () => {
      if (timer !== undefined) window.clearInterval(timer)
      const delay = refreshDelay(hot, document.visibilityState !== 'hidden')
      timer = delay === null
        ? undefined
        : window.setInterval(() => refresh.current(), delay)
    }
    const visibilityChanged = () => {
      if (document.visibilityState !== 'hidden') refresh.current()
      restart()
    }

    restart()
    document.addEventListener('visibilitychange', visibilityChanged)
    return () => {
      document.removeEventListener('visibilitychange', visibilityChanged)
      if (timer !== undefined) window.clearInterval(timer)
    }
  }, [hot])
}
