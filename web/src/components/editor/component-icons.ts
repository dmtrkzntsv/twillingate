import {
  CalendarDaysIcon,
  ChartAreaIcon,
  ChartColumnBigIcon,
  ChartColumnIcon,
  ChartLineIcon,
  ChartPieIcon,
  ChartScatterIcon,
  CircleGaugeIcon,
  FunnelIcon,
  Grid3x3Icon,
  HashIcon,
  LayoutDashboardIcon,
  ListOrderedIcon,
  MapIcon,
  RadarIcon,
  SquareIcon,
  TableIcon,
  TextIcon,
  WorkflowIcon,
  type LucideIcon,
} from 'lucide-react'

/** The icon the visualization select shows beside each component's name. */
export const componentIcons: Record<string, LucideIcon> = {
  area: ChartAreaIcon,
  bar: ChartColumnIcon,
  bar_list: ListOrderedIcon,
  calendar: CalendarDaysIcon,
  combo: ChartColumnBigIcon,
  funnel: FunnelIcon,
  heatmap: Grid3x3Icon,
  line: ChartLineIcon,
  map: MapIcon,
  markdown: TextIcon,
  pie: ChartPieIcon,
  radar: RadarIcon,
  radial: CircleGaugeIcon,
  sankey: WorkflowIcon,
  scatter: ChartScatterIcon,
  stat: HashIcon,
  table: TableIcon,
  treemap: LayoutDashboardIcon,
}

/** A component's icon; a plain square for one added without its own yet. */
export function iconOf(component: string): LucideIcon {
  return componentIcons[component] ?? SquareIcon
}
