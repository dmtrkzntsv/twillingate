import '@testing-library/jest-dom/vitest'

// jsdom has no layout engine: every element measures 0x0. Recharts'
// ResponsiveContainer reads its own box with getBoundingClientRect to size
// the chart, and warns (and renders nothing) when that comes back 0x0. Give
// only that element a fixed, non-zero box, so charts render at a stable
// size in tests; everything else (e.g. the legend's wrapper, which also
// measures itself, to decide how much it should shrink the plot area)
// keeps jsdom's real — zero — measurement. Also stub ResizeObserver, unused
// by jsdom but required by Recharts and some Radix primitives, as a no-op:
// the container box above is read directly on mount, without waiting for a
// resize notification.
const realGetBoundingClientRect = Element.prototype.getBoundingClientRect

class ResizeObserverMock {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}

Object.defineProperty(globalThis, 'ResizeObserver', {
  writable: true,
  configurable: true,
  value: ResizeObserverMock,
})

Element.prototype.getBoundingClientRect = function (this: Element) {
  if (this.classList.contains('recharts-responsive-container')) {
    return {
      width: 500,
      height: 300,
      top: 0,
      left: 0,
      bottom: 300,
      right: 500,
      x: 0,
      y: 0,
      toJSON() {},
    } as DOMRect
  }
  return realGetBoundingClientRect.call(this)
}
