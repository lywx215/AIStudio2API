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
  max_inflight_bytes?: number
  request_timeout_seconds: number
  bindings: BusinessBinding[]
}

export interface BusinessImportPreview {
  revision: number
  source_hash: string
  added: BusinessBinding[]
  skipped: string[]
  warnings: string[]
}

export interface BusinessImportResult {
  config: BusinessConfig
  rollback_id: string
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
  account_states: BusinessAccountState[]
  model_states: BusinessModelState[]
  capacity: { limit: number; active: number; available: number }
  resources?: { limit_bytes: number; in_use_bytes: number; available_bytes: number }
}

export interface BusinessAccountState {
  id: string
  state: string
  auth_verified?: boolean
  auth_verified_at?: string
  verified_models?: Record<string, string>
  last_error_kind?: string
  last_error_stage?: string
  last_error_status?: string
  last_error_reason?: string
  last_error_http_status?: number
  last_error_submitted?: boolean
  last_error_output_started?: boolean
  last_error_at?: string
  consecutive_failures?: number
  cooldown_until?: string
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
