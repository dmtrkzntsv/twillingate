/** The grid's column count; a widget's `width` is out of this many. */
export const COLUMNS = 12

/**
 * A row's height and the gap between cells, in pixels. LayoutGrid's
 * `auto-rows-[40px]` and `gap-3` classes are these same numbers, kept
 * literal because Tailwind needs static class names.
 */
export const ROW_PX = 40
export const GAP_PX = 12

/** The pixel height of a cell spanning `rows` grid rows, gaps included. */
export function rowsPx(rows: number): number {
  return rows * ROW_PX + (rows - 1) * GAP_PX
}

/**
 * The grid width from which every widget spans the columns it defines (D7):
 * a 1280px screen with the sidebar open has a grid of about 976px, which
 * should show a dashboard as its owner laid it out. Below it the spans
 * are narrowed, so the page offers no resizing there either.
 */
export const FULL_GRID_PX = 900

/**
 * How many of the 12 columns a widget spans on a grid `gridPx` wide (D37):
 * as defined from `FULL_GRID_PX`, halves or full rows from 640px, and below that
 * two small widgets side by side or one full row.
 */
export function span(width: number, gridPx: number): number {
  if (gridPx >= FULL_GRID_PX) return width
  if (gridPx >= 640) return width <= 6 ? 6 : COLUMNS
  return width <= 3 ? 6 : COLUMNS
}
