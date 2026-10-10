# Arranging widgets

Status: draft
Date: 2026-10-10

## Problem

- **A widget could not be moved once placed.** The only way was to
  `copy_widget` it with a new `after` and archive the original, which gave
  it a new id and left the archived original behind.
- **Resizing meant asking an agent.** The page showed sizes but never let
  the owner change them, though `update_widget` took `width` and `height`.
- **A layout change re-ran the widget's query.** `update_widget` validated
  the whole widget, so a resize ran the SQL and was refused when the query
  no longer ran, though the content was unchanged.
- **Most laptops never see the layout as designed.** `span` draws widths as
  defined only on a grid of 1024px or more. A 1280px screen with the
  sidebar open has a grid of about 976px, so it shows halves and full rows:
  four 3-wide stats become a 2 × 2 block, and the corner that resizes a
  card (which needs the widths drawn as saved) is not offered there.

## Decisions

Built in PR #157:

- **D1. `update_widget` moves a widget with `after`.** A live widget of the
  same dashboard puts it right after that one, `0` first; naming the widget
  itself is a no-op, as on `update_dashboard`. An archived or foreign
  widget is refused (`ErrInvalid`). Placement reads the order afresh and
  retries once on a key conflict (`widgetKeyAfter`, shared with
  `add_widget` and `copy_widget`).
- **D2. A change of only `width`, `height` and `after` runs no query.** It
  checks the size alone, so a widget whose SQL broke can still be moved and
  resized; any other change validates the whole widget as before.
- **D3. The page arranges a live user dashboard's widgets, always on.** On
  its own page and on a project tab showing it, while the page may write
  (not in reporting dev, not while frozen). No edit mode: the controls are
  revealed on hover like refresh (always on touch screens).
- **D4. Moving: a grip among the card's top-right controls.** Dragging it
  onto another card puts the card at that card's index (after it when
  moving forward, before it when moving back). Cards do not shift during
  the drag, since they differ in size: an outline follows the pointer and a
  bar in the gap shows the side it lands on. Space picks it up from the
  keyboard, arrows move it, Space or Enter drops it, Escape cancels, as in
  every sortable list of the app. Moving works at every grid width.
- **D5. Resizing: the bottom-right corner.** A pointer drag changes the size
  by the columns and rows crossed (rounded), the grid reflowing live and a
  `W × H` badge showing it, saved on release. The arrow keys change width
  (left, right) and height (up, down) by one, saved 600ms after the last
  key or on blur; Escape drops what is unsaved. Bounds 1–12 both ways.
- **D6. Optimistic, then the server's.** A dropped order or size shows until
  the refetched dashboard carries the server's; a refusal is toasted and
  the card snaps back at once.

Added by this spec:

- **D7. Widths are drawn as defined from a 900px grid** (was 1024px). At
  900px a column is 64px and a 3-wide card 216px, enough for a stat or a
  small chart; a 1280px screen with the sidebar open (976px, or about 960
  with a classic scrollbar) now shows the dashboard as designed, and can
  resize. `FULL_GRID_PX = 900` lives in `lib/grid.ts`; `span` and the
  resize corner both read it. Below it nothing changes: halves or full
  rows from 640px, then pairs of small widgets or full rows. The Layout
  section of `docs/reporting.md` says from which width widths are drawn as
  defined, since an agent designing a dashboard needs to know.
- **D8. Screen readers hear the size as it changes.** A polite live region in
  the cell says "<title>: <W> of 12 columns wide, <H> rows tall" while a
  size is being chosen (keys or pointer). Moving already has dnd-kit's
  announcements (titles and positions, `useReorder`).
- **D9. The keyboard move is covered end to end.** A Playwright test moves a
  card with Space, an arrow and Space, and checks the order over the API;
  if dnd-kit's sortable coordinates do not reach the next card in a grid
  whose cards do not shift, that is fixed rather than the test dropped.

Not done, on purpose:

- **No Undo toast** after a move or resize: dragging back undoes it, and a
  toast on every drag would be noise.
- **No resizing below the full-layout width:** the spans there do not show
  the saved width, so a corner would lie about what it saves.
- **No moving a widget to another dashboard by drag:** `copy_widget` does
  that, and tabs as drop targets are a design of their own.
