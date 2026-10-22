export interface BusinessBinding {
  public_id: string
  display_name: string
  selector_mode: 'auto' | 'exact' | 'unresolved'
  upstream_model_id: string
  enabled: boolean
  verification_status: 'verified' | 'unverified'
  evidence: string
  account_ids?: string[]
  generation_parameters?: Record<
    string,
    { upstream_field: string; verified: boolean; evidence: string }
  >
}

export interface BusinessConfig {
  schema_version: 1
  revision: number
  enabled: boolean
  max_concurrent: number
  request_timeout_seconds: number
  bindings: BusinessBinding[]
}

export interface BusinessStatus {
  state: 'disabled' | 'unavailable' | 'closed' | 'ready'
  enabled: boolean
  generation_ready: boolean
  code?: string
  config_revision: number
  transport: string
  capabilities: Record<string, string>
  live_verified: boolean
  account_states: { id: string; state: string }[]
  model_states: BusinessModelState[]
  capacity: { limit: number; active: number; available: number }
}

export interface BusinessModelState {
  public_id: string
  state: 'disabled' | 'unavailable' | 'ready'
  code?: string
  configuration_valid: boolean
  schedulable: boolean
  auth_verified: boolean
  model_verified: boolean
  auth_verified_at?: string
  model_verified_at?: string
  eligible_account_ids: string[]
}

export interface BusinessAccount {
  id: string
  label: string
  enabled: boolean
  epoch: number
  credential_set: boolean
}
export interface BusinessAccounts {
  revision: number
  accounts: BusinessAccount[]
}
export interface BusinessCredential {
  secure_c_ses: string
  host_c_oses: string
  csesidx: string
  config_id: string
  user_agent: string
}
