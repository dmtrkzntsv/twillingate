import { Component, type ReactNode } from 'react'

interface Props {
  /** Shown in place of the children once they throw. */
  fallback: ReactNode
  /** What the children draw from: a change to any of these tries them again. */
  resetKeys: readonly unknown[]
  children: ReactNode
}

interface State {
  failed: boolean
  keys: readonly unknown[]
}

const same = (a: readonly unknown[], b: readonly unknown[]) => a.length === b.length && a.every((v, i) => Object.is(v, b[i]))

/**
 * Keeps one widget's throw inside its own frame. A component drawn from
 * rows it cannot read (a column it expects is missing, a day that is not
 * one) would otherwise unmount the whole console, and with it any draft.
 */
export default class WidgetErrorBoundary extends Component<Props, State> {
  state: State = { failed: false, keys: this.props.resetKeys }

  static getDerivedStateFromError(): Partial<State> {
    return { failed: true }
  }

  static getDerivedStateFromProps(props: Props, state: State): Partial<State> | null {
    return same(props.resetKeys, state.keys) ? null : { failed: false, keys: props.resetKeys }
  }

  render() {
    return this.state.failed ? this.props.fallback : this.props.children
  }
}
