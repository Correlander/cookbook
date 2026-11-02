import type { Reporter, TestCase, TestResult } from '@playwright/test/reporter'

function redact(value: string): string {
  return value.replace(/([?&](?:token|invite)=)[^&#\s]*/gi, '$1[redacted]')
}

class SafeReporter implements Reporter {
  onTestEnd(test: TestCase, result: TestResult): void {
    const label = result.status === 'passed' ? 'PASS' : result.status === 'skipped' ? 'SKIP' : 'FAIL'
    console.log(`${label} ${redact(test.title)}`)
    if (result.status === 'failed') console.error(redact(result.errors.map((error) => error.message ?? '').join('\n')))
  }
  onEnd(result: { status: string }): void { console.log(`Playwright result: ${result.status}`) }
}
export default SafeReporter
