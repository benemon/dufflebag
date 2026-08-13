export function updateBulkSelection(
  current: string[], ids: string[], isSelecting: boolean,
): string[] {
  if (!isSelecting) {
    const removed = new Set(ids)
    return current.filter((id) => !removed.has(id))
  }
  return [...new Set([...current, ...ids])]
}
