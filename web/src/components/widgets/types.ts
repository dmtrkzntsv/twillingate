import type { ComponentType } from 'react'
import type { Filter, TableView } from '@/lib/table-view'

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
  // The rest is read by `table` only.
  /** The viewer's filters, sort and page; absent, the table keeps its own (the gallery). */
  view?: TableView
  onView?: (next: TableView) => void
  /** Remote mode: the card's loader for distinct values; absent, they come from the loaded rows. */
  fetchDistinct?: (column: string, filters: Filter[]) => Promise<{ value: string; rows: number; capped: boolean }[]>
  /** Remote mode: what the server answered for this page. */
  page?: { offset: number; limit: number; matched: number; total: number }
  /** Remote mode: the server refused the last view (shown under the bar). */
  viewError?: string
  /** A later load is in flight: the current rows stay, dimmed. */
  reloading?: boolean
  /**
   * Makes each body row a button that calls this with the row's index in
   * the page shown (a form's submissions open their drawer). Not part of
   * any widget's contract: a dashboard never sets it.
   */
  onRow?: (index: number) => void
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
  // Rendering `null` covers an empty result: "no data" is the card's job
  // (Task 19), not the widget's. A heavy one is `React.lazy`, so whoever
  // renders it provides the Suspense boundary.
  default: ComponentType<WidgetProps<any>>
}
