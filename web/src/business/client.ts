import { requestJSON } from '@/api'
import type { BusinessConfig, BusinessStatus, BusinessAccounts, BusinessCredential } from './types'

const root = '/api/addons/business'

// The existing request helper handles expiration of the shared console session.
// No model API key or Google credential is stored in this UI client.
export const businessClient = {
  status: (signal: AbortSignal) => requestJSON<BusinessStatus>(`${root}/status`, { signal }),
  config: (signal: AbortSignal) => requestJSON<BusinessConfig>(`${root}/config`, { signal }),
  save: (config: BusinessConfig, signal: AbortSignal) =>
    requestJSON<BusinessConfig>(`${root}/config`, {
      method: 'PUT',
      body: JSON.stringify(config),
      signal,
    }),
  accounts: (signal: AbortSignal) => requestJSON<BusinessAccounts>(`${root}/accounts`, { signal }),
  saveAccount: (
    input: {
      revision: number
      id?: string
      label: string
      enabled: boolean
      credential?: BusinessCredential
    },
    signal: AbortSignal,
  ) =>
    requestJSON<BusinessAccounts>(`${root}/accounts`, {
      method: 'POST',
      body: JSON.stringify(input),
      signal,
    }),
  deleteAccount: (revision: number, id: string, signal: AbortSignal) =>
    requestJSON<BusinessAccounts>(`${root}/accounts/delete`, {
      method: 'POST',
      body: JSON.stringify({ revision, id }),
      signal,
    }),
  collectAccount: (
    input: { revision: number; id?: string; label: string; executable_path: string },
    signal: AbortSignal,
  ) =>
    requestJSON<BusinessAccounts>(`${root}/collect`, {
      method: 'POST',
      body: JSON.stringify(input),
      signal,
    }),
  tryGenerate: (publicID: string, body: string, signal: AbortSignal) =>
    requestJSON<unknown>(`${root}/try/models/${encodeURIComponent(publicID)}:generateContent`, {
      method: 'POST',
      body,
      signal,
    }),
}
