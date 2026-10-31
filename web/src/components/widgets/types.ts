import type { ComponentType } from 'react'
import type { OptionLoader } from '@/components/table-filters'
import type { MarkdownData, PageInfo, SqlData } from '@/lib/api'
import type { TableView } from '@/lib/table-view'

export type { MarkdownData, SqlData }

type ColumnType = 'number' | 'text' | 'day'

interface InputColumn {
  name: string
  types: ColumnType[]
  optional?: boolean
}

export interface Contract {
  description: string
  accepts: ('sql' | 'query' | 'md')[]
  inputs: { open: boolean; columns: InputColumn[] }
  props: Record<string, unknown> // JSON schema, additionalProperties false
  defaultWidth: number
  defaultHeight: number
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
  fetchDistinct?: OptionLoader
  /** Remote mode: what the server answered for this page. */
  page?: Pick<PageInfo, 'offset' | 'limit' | 'matched' | 'total'>
  /** Remote mode: the server refused the last view (shown under the bar). */
  viewError?: string
  /** A later load is in flight: the current rows stay, dimmed. */
  reloading?: boolean
  /**
   * Remote mode only (`props.mode === 'remote'`; ignored otherwise): makes
   * each body row a button that calls this with the row's index in the
   * page the server answered, the index of its id beside the rows (a
   * form's submissions open their drawer). A local table re-sorts and
   * filters in the browser, so its index would name no server row. Not
   * part of any widget's contract: a dashboard never sets it.
   */
  onRow?: (index: number) => void
  /**
   * With onRow, the same way: a checkbox before each body row and one in
   * the header for every row of the page, by index in the page. The row
   * then opens from a button in its first cell, stretched over the row,
   * rather than being a button itself. Not part of any widget's contract.
   */
  selection?: { selected: ReadonlySet<number>; onChange: (next: Set<number>) => void }
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
