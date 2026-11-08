import { describe, expect, it } from 'vitest'
import { formAfter, formNameProblem, formSnippet } from './forms'

describe('formNameProblem', () => {
  it('takes what the ingest path takes, and says nothing while empty', () => {
    for (const ok of ['', 'contact', 'sign-up_2', 'a'.repeat(64)]) expect(formNameProblem(ok)).toBe('')
    for (const bad of ['Contact', 'sign up', 'a/b', '$x', 'é', 'a'.repeat(65)]) expect(formNameProblem(bad)).not.toBe('')
  })
})

describe('formAfter', () => {
  const names = ['a', 'b', 'c', 'd']
  it('names the form before the new place, empty for first', () => {
    expect(formAfter(names, 'c', 0)).toBe('')
    expect(formAfter(names, 'a', 1)).toBe('b')
    expect(formAfter(names, 'a', 3)).toBe('d')
    expect(formAfter(names, 'd', 1)).toBe('a')
    expect(formAfter(names, 'b', 99)).toBe('d')
  })
})

describe('formSnippet', () => {
  it("is today's contact snippet for a form with no fields", () => {
    expect(formSnippet('contact')).toBe(`<form data-twillingate-form="contact">
  <input name="email" type="email" required>
  <textarea name="message"></textarea>
  <button>Send</button>
</form>`)
  })

  it('names the form and one control per field, escaped', () => {
    expect(formSnippet('signup', ['email', 'company', 'a"b'])).toBe(`<form data-twillingate-form="signup">
  <input name="email" type="email" required>
  <input name="company">
  <input name="a&quot;b">
  <button>Send</button>
</form>`)
  })
})
