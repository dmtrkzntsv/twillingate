import ReactMarkdown from 'react-markdown'
import type { Contract, MarkdownData, WidgetProps } from './types'

export const contract: Contract = {
  description: 'Free-form text: a note, a caveat, instructions for reading the dashboard.',
  accepts: ['md'],
  inputs: { open: false, columns: [] },
  props: { type: 'object', properties: {}, additionalProperties: false },
  defaultWidth: 12,
  defaultHeight: 2,
}

export default function Markdown({ data }: WidgetProps) {
  const md = data as MarkdownData
  if (!md.markdown || md.markdown.trim() === '') return null

  return (
    <div className="prose-widget h-full overflow-auto p-2 text-sm">
      {/* No rehype-raw: any raw HTML in the text is neutralized, never rendered as an element. */}
      <ReactMarkdown>{md.markdown}</ReactMarkdown>
    </div>
  )
}
