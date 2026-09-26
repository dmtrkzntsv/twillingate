import { PRESETS, type Preset } from './ranges'

/** What a dashboard page shows: the project and/or the date range. */
export interface Selection {
  projectId?: number
  range?: Preset
  from?: string
  to?: string
}

interface StoredSelection {
  project_id?: number
  range?: string
  from?: string
  to?: string
}

interface Switchers {
  project: boolean
  range: boolean
}

const PRESET_IDS = new Set<string>(PRESETS.map((p) => p.id))

function isValidRange(range: string | null | undefined, from?: string | null, to?: string | null): range is Preset {
  if (!range || !PRESET_IDS.has(range)) return false
  return range !== 'custom' || (!!from && !!to)
}

function parseProjectId(v: string | null): number | undefined {
  if (v === null || v === '') return undefined
  const n = Number(v)
  return Number.isFinite(n) ? n : undefined
}

/**
 * Picks the project and/or range a dashboard shows (D34): the URL wins,
 * then the stored selection (if its project is still active), then the
 * first active project with the 7-day range. A part is left out entirely
 * when the dashboard has no switcher for it.
 */
export function chooseSelection(
  url: URLSearchParams,
  stored: StoredSelection,
  activeProjects: number[],
  switchers: Switchers
): Selection {
  const selection: Selection = {}

  if (switchers.project) {
    const fromURL = parseProjectId(url.get('project'))
    if (fromURL !== undefined && activeProjects.includes(fromURL)) {
      selection.projectId = fromURL
    } else if (stored.project_id !== undefined && activeProjects.includes(stored.project_id)) {
      selection.projectId = stored.project_id
    } else if (activeProjects.length > 0) {
      selection.projectId = activeProjects[0]
    }
  }

  if (switchers.range) {
    const urlRange = url.get('range')
    const urlFrom = url.get('from')
    const urlTo = url.get('to')
    if (isValidRange(urlRange, urlFrom, urlTo)) {
      selection.range = urlRange
      if (urlRange === 'custom') {
        selection.from = urlFrom ?? undefined
        selection.to = urlTo ?? undefined
      }
    } else if (isValidRange(stored.range, stored.from, stored.to)) {
      selection.range = stored.range as Preset
      if (stored.range === 'custom') {
        selection.from = stored.from
        selection.to = stored.to
      }
    } else {
      selection.range = '7d'
    }
  }

  return selection
}
