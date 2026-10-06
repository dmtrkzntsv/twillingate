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
