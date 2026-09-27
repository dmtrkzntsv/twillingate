import type { WidgetModule } from './types'
import * as area from './area'
import * as bar from './bar'
import * as bar_list from './bar_list'
import * as calendar from './calendar'
import * as combo from './combo'
import * as funnel from './funnel'
import * as heatmap from './heatmap'
import * as line from './line'
import * as map from './map'
import * as markdown from './markdown'
import * as pie from './pie'
import * as radar from './radar'
import * as radial from './radial'
import * as scatter from './scatter'
import * as stat from './stat'
import * as table from './table'
import * as treemap from './treemap'

export const widgets: Record<string, WidgetModule> = {
  area,
  bar,
  bar_list,
  calendar,
  combo,
  funnel,
  heatmap,
  line,
  map,
  markdown,
  pie,
  radar,
  radial,
  scatter,
  stat,
  table,
  treemap,
}
