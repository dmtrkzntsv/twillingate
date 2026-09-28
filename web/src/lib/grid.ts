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
 * How many of the 12 columns a widget spans on a grid `gridPx` wide (D37):
 * as defined from 1024px, halves or full rows from 640px, and below that
 * two small widgets side by side or one full row.
 */
export function span(width: number, gridPx: number): number {
  if (gridPx >= 1024) return width
  if (gridPx >= 640) return width <= 6 ? 6 : COLUMNS
  return width <= 3 ? 6 : COLUMNS
}
