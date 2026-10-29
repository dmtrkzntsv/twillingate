import { describe, expect, it } from 'vitest'
import { toCSV } from './forms'

describe('toCSV', () => {
  it('writes the header and rows, quoting what needs it', () => {
    expect(toCSV(['a', 'b'], [['x', 'say "hi", then go'], ['line\nbreak', '']])).toBe(
      'a,b\r\nx,"say ""hi"", then go"\r\n"line\nbreak",\r\n'
    )
  })

  it('keeps a spreadsheet from running a cell as a formula, as the server export does', () => {
    expect(toCSV(['=h'], [['=SUM(A1)'], ['+1'], ['-2'], ['@x'], ['\tt'], ['ok']])).toBe("'=h\r\n'=SUM(A1)\r\n'+1\r\n'-2\r\n'@x\r\n'\tt\r\nok\r\n")
  })
})
