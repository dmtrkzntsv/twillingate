import type { WidgetModule } from './types'
import * as area from './area'
import * as bar from './bar'
import * as bar_list from './bar_list'
import * as combo from './combo'
import * as line from './line'
import * as pie from './pie'
import * as radar from './radar'
import * as radial from './radial'
import * as scatter from './scatter'
import * as stat from './stat'

export const widgets: Record<string, WidgetModule> = {
  area,
  bar,
  bar_list,
  combo,
  line,
  pie,
  radar,
  radial,
  scatter,
  stat,
}
