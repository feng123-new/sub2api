import type { CodexTicketStatus } from '@/api/admin/accounts'

export function codexTicketCurrentState(status?: CodexTicketStatus): 'rejected' | 'missing' | 'verified' | 'pending' | 'unknown' {
  if (status?.validation_rejected) return 'rejected'
  if (!status?.cached) return 'missing'
  if (status.validation_outcome === 'remote_completed') return 'verified'
  if (!status.validation_outcome) return 'pending'
  return 'unknown'
}

export function codexTicketCurrentClass(status?: CodexTicketStatus): string {
  const state = codexTicketCurrentState(status)
  if (state === 'rejected') return 'text-red-700 dark:text-red-400'
  if (state === 'verified') return 'text-emerald-700 dark:text-emerald-400'
  return 'text-amber-700 dark:text-amber-400'
}
