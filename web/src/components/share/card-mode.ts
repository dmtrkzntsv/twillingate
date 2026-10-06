import { createContext, useContext } from 'react'

/**
 * True while a component draws on a share card (D4): no tooltips, menus,
 * filters, pagination or scrollbars, and type sized for a card seen small
 * in a feed. A component that needs to know reads `useCardMode()`.
 */
export const CardMode = createContext(false)

export const useCardMode = () => useContext(CardMode)

/** The card in CSS pixels; captured at pixel ratio 1 (og:image) and 2. */
export const CARD = { width: 1200, height: 630 } as const

/**
 * A card's small type, in px: axis ticks, legends, labels and notes, about
 * 1.6 times the dashboard's 11px, for a card seen small in a feed. ShareCard
 * sets it as `--card-type`, which index.css and the components' classes read.
 */
export const CARD_TYPE = 18

/** The least room between two x-axis ticks on a card: fewer ticks at the larger type. */
export const CARD_TICK_GAP = 56
