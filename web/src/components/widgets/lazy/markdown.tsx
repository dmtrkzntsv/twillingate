import ReactMarkdown from 'react-markdown'
import type { MarkdownData, WidgetProps } from '../types'

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
