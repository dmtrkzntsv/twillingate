/** Bytes in decimal units: 999 B, 2.3 MB, 2.3 GB. */
export function formatBytes(n: number): string {
  if (n < 1000) return `${n} B`
  const units = ['kB', 'MB', 'GB', 'TB']
  let v = n
  let i = -1
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
}

/** How long ago an ISO time was: just now, 2 min ago, 3 h ago, 3 days ago. */
export function formatAgo(iso: string, now: Date = new Date()): string {
  const s = Math.max(0, (now.getTime() - new Date(iso).getTime()) / 1000)
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)} min ago`
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`
  const d = Math.floor(s / 86400)
  return `${d} ${d === 1 ? 'day' : 'days'} ago`
}
