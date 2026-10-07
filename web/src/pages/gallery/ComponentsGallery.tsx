import { useSearchParams } from 'react-router'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { widgets } from '@/components/widgets'
import ComponentEntry, { type GalleryView } from './ComponentEntry'
import GalleryLayout from './GalleryLayout'

const names = Object.keys(widgets).sort()

/**
 * `/gallery/components`: every component, rendered from its examples, to
 * point an agent at by name. `?view=cards` draws each example as its share
 * card instead (D4), at its real proportions, in the gallery's theme: the
 * place where the cards' look gets judged.
 */
export default function ComponentsGallery() {
  const [params, setParams] = useSearchParams()
  const view: GalleryView = params.get('view') === 'cards' ? 'cards' : 'tiles'
  const setView = (next: string) => {
    if (next !== 'tiles' && next !== 'cards') return
    setParams(
      (prev) => {
        const p = new URLSearchParams(prev)
        if (next === 'cards') p.set('view', 'cards')
        else p.delete('view')
        return p
      },
      { replace: true }
    )
  }
  return (
    <GalleryLayout
      title="Components"
      description="Every component a dashboard widget can use, drawn with sample data. Ask your agent for one by name, or paste its add_widget JSON."
      sections={names.map((name) => ({ id: `component-${name}`, label: name }))}
    >
      <ToggleGroup type="single" variant="outline" size="sm" value={view} onValueChange={setView} aria-label="View">
        <ToggleGroupItem value="tiles">Tiles</ToggleGroupItem>
        <ToggleGroupItem value="cards">Share cards</ToggleGroupItem>
      </ToggleGroup>
      {names.map((name) => (
        <ComponentEntry key={name} name={name} module={widgets[name]} view={view} />
      ))}
    </GalleryLayout>
  )
}
