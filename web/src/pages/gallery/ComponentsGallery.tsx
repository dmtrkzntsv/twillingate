import { widgets } from '@/components/widgets'
import ComponentEntry from './ComponentEntry'
import GalleryLayout from './GalleryLayout'

const names = Object.keys(widgets).sort()

/** `/gallery/components`: every component, rendered from its examples, to point an agent at by name. */
export default function ComponentsGallery() {
  return (
    <GalleryLayout
      title="Components"
      description="Every component a dashboard widget can use, drawn with sample data. Ask your agent for one by name, or paste its add_widget JSON."
      sections={names.map((name) => ({ id: `component-${name}`, label: name }))}
    >
      {names.map((name) => (
        <ComponentEntry key={name} name={name} module={widgets[name]} />
      ))}
    </GalleryLayout>
  )
}
