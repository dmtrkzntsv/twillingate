import ReactMarkdown from 'react-markdown'
import { useCardMode } from '@/components/share/card-mode'
import type { MarkdownData, WidgetProps } from '../types'

export default function Markdown({ data }: WidgetProps) {
  const md = data as MarkdownData
  // On a share card, set in the card's type (index.css sizes its headings).
  const card = useCardMode()
  if (!md.markdown || md.markdown.trim() === '') return null

  return (
    <div className={`prose-widget h-full p-2 ${card ? 'overflow-hidden text-[24px]' : 'overflow-auto text-sm'}`}>
      {/* No rehype-raw: any raw HTML in the text is neutralized, never rendered as an element. */}
      <ReactMarkdown>{md.markdown}</ReactMarkdown>
    </div>
  )
}
