/**
 * Light and dark follow the system: shadcn's `.dark` class on <html>, set
 * from `prefers-color-scheme` and kept in step when the system changes.
 */
export function followSystemTheme(): void {
  const dark = window.matchMedia('(prefers-color-scheme: dark)')
  const apply = () => document.documentElement.classList.toggle('dark', dark.matches)
  apply()
  dark.addEventListener('change', apply)
}
