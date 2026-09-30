import type { ReactElement } from 'react'

export type ColumnType = 'number' | 'text' | 'day'

export interface InputColumn {
  name: string
  types: ColumnType[]
  optional?: boolean
}

export interface Contract {
  description: string
  accepts: ('sql' | 'md')[]
  inputs: { open: boolean; columns: InputColumn[] }
  props: Record<string, unknown> // JSON schema, additionalProperties false
  defaultWidth: number
  defaultHeight: number
}

export interface SqlData {
  columns: string[]
  rows: string[][]
  truncated: boolean
}

export interface MarkdownData {
  markdown: string
}

export interface WidgetProps<P = Record<string, unknown>> {
  data: SqlData | MarkdownData
  props: P
  /** A localStorage key prefix for what a viewer changes on this widget; absent in the gallery. */
  stateKey?: string
}

/**
 * A worked example the gallery renders: a result shaped exactly as the
 * server returns it, and the props to show it with.
 */
export interface Example {
  title: string
  props: Record<string, unknown>
  data: SqlData | MarkdownData
}

export interface WidgetModule {
  contract: Contract
  /** At least one; the gallery shows each at the default size. */
  examples: Example[]
  // `null` covers an empty result: "no data" is the card's job (Task 19),
  // not the widget's.
  default: (p: WidgetProps<any>) => ReactElement | null
}
