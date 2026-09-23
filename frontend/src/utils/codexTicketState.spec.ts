import { describe, expect, it } from 'vitest'
import { codexTicketCurrentState } from './codexTicketState'

describe('current ticket display semantics', () => {
  const cached = { model: 'gpt-6-astra', ready: true, cached: true, remaining_seconds: 3600, blocked: false }
  it('does not equate unexpired cache with successful validation', () => {
    expect(codexTicketCurrentState(cached)).toBe('pending')
    expect(codexTicketCurrentState({ ...cached, validation_outcome: 'remote_completed' })).toBe('verified')
  })
  it('shows rejection independently of cache expiry and latest transient check', () => {
    expect(codexTicketCurrentState({ ...cached, ready: false, validation_rejected: true, validation_outcome: 'validation_timeout' })).toBe('rejected')
    expect(codexTicketCurrentState({ ...cached, validation_outcome: 'validation_timeout' })).toBe('unknown')
  })
  it('does not show expired verified state as currently usable', () => {
    expect(codexTicketCurrentState({ ...cached, cached: false, validation_outcome: 'remote_completed' })).toBe('missing')
  })
})
