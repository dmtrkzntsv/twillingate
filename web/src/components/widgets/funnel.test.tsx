import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { CardMode } from '@/components/share/card-mode'
import type { SqlData, WidgetProps } from './types'
import Funnel, { contract } from './funnel'

function renderFunnel(data: SqlData, props: WidgetProps['props'] = {}) {
  return render(
    <div style={{ width: 400, height: 400 }}>
      <Funnel data={data} props={props} />
    </div>
  )
}

describe('funnel', () => {
  it('renders one row per step, in query order', () => {
    const { container } = renderFunnel({
      columns: ['step', 'value'],
      rows: [
        ['visit', '100'],
        ['signup', '50'],
        ['purchase', '25'],
      ],
      truncated: false,
    })
    const steps = container.querySelectorAll('[data-funnel-step]')
    expect(steps).toHaveLength(3)
    ;['visit', 'signup', 'purchase'].forEach((name, i) => expect(steps[i]).toHaveTextContent(name))
  })

  it("labels each step with its share of the first step's value", () => {
    const { container } = renderFunnel({
      columns: ['step', 'value'],
      rows: [
        ['visit', '100'],
        ['signup', '50'],
        ['purchase', '25'],
      ],
      truncated: false,
    })
    const scope = within(container)
    expect(scope.getByText('visit')).toBeInTheDocument()
    expect(scope.getByText('100%')).toBeInTheDocument()
    expect(scope.getByText('signup')).toBeInTheDocument()
    expect(scope.getByText('50%')).toBeInTheDocument()
    expect(scope.getByText('purchase')).toBeInTheDocument()
    expect(scope.getByText('25%')).toBeInTheDocument()
  })

  it('says how much of the step before each step converts', () => {
    const { container } = renderFunnel({
      columns: ['step', 'value'],
      rows: [
        ['visit', '200'],
        ['signup', '50'],
        ['purchase', '40'],
      ],
      truncated: false,
    })
    const scope = within(container)
    expect(scope.getByText('25% converted')).toBeInTheDocument()
    expect(scope.getByText('80% converted')).toBeInTheDocument()
  })

  it('renders nothing broken for an empty result', () => {
    const { container } = renderFunnel({ columns: ['step', 'value'], rows: [], truncated: false })
    expect(container.firstChild).toBeEmptyDOMElement()
  })

  it('marks approximate values and the drop between them with ≈ on hover', async () => {
    const user = userEvent.setup()
    renderFunnel({
      columns: ['step', 'value'],
      rows: [
        ['visit', '20000'],
        ['signup', '5000'],
      ],
      truncated: false,
      approximate: ['value'],
    })
    await user.hover(screen.getByText('signup'))
    const card = screen.getByRole('tooltip')
    expect(card).toHaveTextContent('Value≈5,000')
    expect(card).toHaveTextContent('Dropped≈15,000')
    expect(card).toHaveTextContent('Of the first step25%')
  })

  it('shows the step value, conversion and drop-off on hover', async () => {
    const user = userEvent.setup()
    renderFunnel({
      columns: ['step', 'value'],
      rows: [
        ['visit', '20000'],
        ['signup', '5000'],
      ],
      truncated: false,
    })
    const signup = screen.getByText('signup')
    await user.hover(signup)
    const card = screen.getByRole('tooltip')
    expect(card).toHaveTextContent('signup')
    expect(card).toHaveTextContent('Value5,000')
    expect(card).toHaveTextContent('Of the first step25%')
    expect(card).toHaveTextContent('Of the step before25%')
    expect(card).toHaveTextContent('Dropped15,000')

    await user.unhover(signup)
    await user.hover(screen.getByText('visit'))
    expect(screen.getByRole('tooltip')).not.toHaveTextContent('step before')
  })

  describe('on a share card', () => {
    afterEach(() => vi.restoreAllMocks())

    const sixSteps: SqlData = {
      columns: ['step', 'value'],
      rows: [
        ['visit', '200'],
        ['signup', '50'],
        ['onboarded', '40'],
        ['activated', '30'],
        ['paid', '20'],
        ['renewed', '10'],
      ],
      truncated: false,
    }

    function renderCard() {
      return render(
        <CardMode.Provider value={true}>
          <Funnel data={sixSteps} props={{}} />
        </CardMode.Provider>
      )
    }

    // jsdom lays nothing out: the list's height is what the stub says it is.
    function listHeight(height: (el: HTMLElement) => number) {
      vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(360)
      vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockImplementation(function (this: HTMLElement) {
        return this.tagName === 'OL' ? height(this) : 0
      })
    }

    it('keeps the roomy layout, a note in every neck, while the steps fit', () => {
      listHeight(() => 360)
      const { container } = renderCard()
      expect(container.querySelector('ol')).toHaveClass('justify-center-safe')
      expect(within(container).getByText('25% converted')).toBeInTheDocument()
      expect(container.querySelector('[data-funnel-step] .flex-col')).not.toBeNull()
    })

    it('puts each label on one line before dropping the neck notes', () => {
      // Too tall only in the roomy layout, with its two-line labels.
      listHeight((ol) => (ol.querySelector('[data-funnel-step] .flex-col') ? 420 : 360))
      const { container } = renderCard()
      expect(container.querySelector('[data-funnel-step] .flex-col')).toBeNull()
      expect(within(container).getByText('25% converted')).toBeInTheDocument()
    })

    it('drops the neck notes and shrinks the names when the steps still do not fit', () => {
      listHeight(() => 500)
      const { container } = renderCard()
      expect(within(container).queryByText(/converted/)).toBeNull()
      expect(within(container).getByText('visit').parentElement).toHaveClass('text-[length:var(--card-type)]')
      // Still every step, in order: denser, never fewer.
      expect(container.querySelectorAll('[data-funnel-step]')).toHaveLength(6)
    })
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql', 'query'])
    expect(contract.defaultWidth).toBe(6)
    expect(contract.defaultHeight).toBe(8)
  })
})
