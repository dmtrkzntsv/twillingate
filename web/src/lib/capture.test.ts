import { afterEach, describe, expect, it } from 'vitest'
import { inlineSvgStyles } from './capture'

afterEach(() => {
  document.head.innerHTML = ''
  document.body.innerHTML = ''
})

describe('inlineSvgStyles', () => {
  it('writes the stroke a class gives an SVG mark into its own style', () => {
    const style = document.createElement('style')
    style.textContent = '.faint line { stroke: rgb(1, 2, 3); }'
    document.head.append(style)
    document.body.innerHTML = '<div class="faint"><svg><g><line stroke="#ccc"></line></g></svg><p>text</p></div>'
    inlineSvgStyles(document.body.firstElementChild as HTMLElement)
    expect(document.querySelector('line')?.getAttribute('style')).toContain('stroke: rgb(1, 2, 3)')
    // Only SVG descendants: html-to-image inlines HTML elements on its own.
    expect(document.querySelector('p')?.getAttribute('style')).toBeNull()
  })
})
