import axios from 'axios';
import type { AxiosError, InternalAxiosRequestConfig } from 'axios';
import { getAdminAccessToken, refreshAdminAccessToken } from '../auth/adminAuth';

export const atryumApi = axios.create({
  headers: { 'Content-Type': 'application/json' },
});

export const apiErrorMessage = (err: unknown, fallback: string): string => {
  if (typeof err === 'object' && err !== null && 'response' in err) {
    const data = (err as {
      response?: { data?: { error?: string | { message?: unknown } } };
    }).response?.data;
    const apiError = data?.error;
    if (typeof apiError === 'string' && apiError) return apiError;
    if (
      typeof apiError === 'object' &&
      apiError !== null &&
      typeof apiError.message === 'string' &&
      apiError.message
    ) {
      return apiError.message;
    }
  }
  if (err instanceof Error && err.message) return err.message;
  if (typeof err === 'object' && err !== null && 'message' in err) {
    const msg = (err as { message: unknown }).message;
    if (typeof msg === 'string' && msg) return msg;
  }
  return fallback;
};

type RetryableRequestConfig = InternalAxiosRequestConfig & {
  _atryumAuthRetried?: boolean;
};

atryumApi.interceptors.request.use(async (config) => {
  const token = await getAdminAccessToken();
  if (token) {
    config.headers.Authorization = `Bearer ${token}`;
  }
  return config;
});

atryumApi.interceptors.response.use(
  (response) => response,
  async (error: AxiosError) => {
    const config = error.config as RetryableRequestConfig | undefined;
    if (error.response?.status !== 401 || !config || config._atryumAuthRetried) {
      throw error;
    }
    const token = await refreshAdminAccessToken();
    if (!token) throw error;
    config._atryumAuthRetried = true;
    config.headers.Authorization = `Bearer ${token}`;
    return atryumApi.request(config);
  },
);

// ─── Invocations ─────────────────────────────────────────────────────────────

export type InvocationStatus =
  | 'pending_approval'
  | 'approved'
  | 'denied'
  | 'received'
  | 'executing'
  | 'succeeded'
  | 'failed'
  | 'expired'
  | 'cancelled';

export interface InvocationApproval {
  status: string;
  request_id?: string | null;
  expires_at?: string | null;
  reason?: string | null;
  actor_id?: string | null;
  decision_at?: string | null;
  confidence_score?: number | null;
}

export interface Invocation {
  invocation_id: string;
  server_name: string;
  tool_name: string;
  status: InvocationStatus;
  submitted_at: string;
  completed_at?: string | null;
  approval?: InvocationApproval | null;
  request_id?: string | null;
  matched_rule_id?: string | null;
  /** Set when an approved plan's pass auto-approved this invocation. */
  plan_id?: string | null;
  agent_id?: string | null;
  summary?: string;
  agent_client_name?: string | null;
  agent_client_version?: string | null;
  user_id?: string | null;
}

export interface InvocationDetail extends Invocation {
  input?: unknown;
  result?: unknown;
  error?: unknown;
}

export interface InvocationEvent {
  type: string;
  timestamp: string;
  data?: unknown;
}

export interface InvocationFilters {
  server?: string;
  tool?: string;
  status?: string;
  client_name?: string;
  offset?: number;
  limit?: number;
}

export const invocationsApi = {
  list: async (
    filters: InvocationFilters = {},
  ): Promise<{ items: Invocation[]; total: number; offset: number; limit: number }> => {
    const params = new URLSearchParams();
    if (filters.server) params.set('server', filters.server);
    if (filters.tool) params.set('tool', filters.tool);
    if (filters.status) params.set('status', filters.status);
    if (filters.client_name) params.set('client_name', filters.client_name);
    if (filters.offset != null) params.set('offset', String(filters.offset));
    params.set('limit', String(filters.limit ?? 50));
    const { data } = await atryumApi.get(
      `/api/v1/admin/invocations?${params.toString()}`,
    );
    return data;
  },

  detail: async (id: string): Promise<InvocationDetail> => {
    const { data } = await atryumApi.get(
      `/api/v1/admin/invocations/${encodeURIComponent(id)}`,
    );
    return data;
  },

  events: async (id: string): Promise<{ items: InvocationEvent[] }> => {
    const { data } = await atryumApi.get(
      `/api/v1/admin/invocations/${encodeURIComponent(id)}/events?limit=200`,
    );
    return data;
  },

  approve: async (
    id: string,
    body?: { create_rule?: RuleInput },
  ): Promise<void> => {
    await atryumApi.post(
      `/api/v1/admin/invocations/${encodeURIComponent(id)}/approve`,
      body ?? {},
    );
  },

  deny: async (
    id: string,
    message?: string,
    createRule?: RuleInput,
  ): Promise<void> => {
    const body: { message?: string; create_rule?: RuleInput } = {};
    if (message) body.message = message;
    if (createRule) body.create_rule = createRule;
    await atryumApi.post(
      `/api/v1/admin/invocations/${encodeURIComponent(id)}/deny`,
      body,
    );
  },

  summarize: async (
    id: string,
    modelConfigCuid?: string,
  ): Promise<{ summary: string }> => {
    const body = modelConfigCuid ? { model_config_cuid: modelConfigCuid } : {};
    const { data } = await atryumApi.post(
      `/api/v1/admin/invocations/${encodeURIComponent(id)}/summarize`,
      body,
    );
    return data;
  },
};

// ─── Servers ──────────────────────────────────────────────────────────────────

export type ConnectionStatus = 'ready' | 'unreachable' | 'unknown';
export type AuthStatus =
  | 'ok'
  | 'missing_credentials'
  | 'invalid'
  | 'not_tested'
  | 'reauth_needed';
export type ServerMode = 'http' | 'stdio';

export interface AuthHeader {
  name: string;
  value: string;
}

export interface Server {
  name: string;
  endpoint_slug: string;
  endpoint_url?: string;
  mode: ServerMode;
  connection_status: ConnectionStatus;
  auth_status: AuthStatus;
  auth_type?: string;
  enabled: boolean;
  base_url?: string;
  timeout_seconds?: number;
  auth_token?: string;
  auth_headers?: AuthHeader[];
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  oauth_provider_id?: string;
  oauth_provider_label?: string;
  oauth_client_registration?: 'preshared' | 'dynamic' | 'cimd' | '';
  oauth_client_id?: string;
  oauth_authorize_url?: string;
  oauth_token_url?: string;
  oauth_scopes?: string;
  oauth_granted_scopes?: string;
  has_oauth_client_secret?: boolean;
  reauth_needed?: boolean;
  action_required?: string | null;
  last_error_summary?: string | null;
  last_checked_at?: string | null;
  last_check_ok?: boolean;
}

export interface ServerInput {
  name: string;
  mode: ServerMode;
  enabled: boolean;
  base_url?: string;
  timeout_seconds?: number;
  auth_token?: string;
  auth_headers?: AuthHeader[];
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  oauth_client_id?: string;
  oauth_client_secret?: string;
  oauth_authorize_url?: string;
  oauth_token_url?: string;
  oauth_scopes?: string;
}

export interface OAuthConnectStartResponse {
  connect_url: string;
  state: string;
}

export interface OAuthConnectStatusResponse {
  status: string;
  message?: string | null;
}

export interface ServerTool {
  name: string;
  description?: string;
}

export const serversApi = {
  list: async (showDisabled = true): Promise<{ items: Server[] }> => {
    const params = new URLSearchParams({ limit: '100' });
    if (!showDisabled) params.set('enabled', 'true');
    const { data } = await atryumApi.get(
      `/api/v1/admin/servers?${params.toString()}`,
    );
    return data;
  },

  detail: async (name: string): Promise<Server> => {
    const { data } = await atryumApi.get(
      `/api/v1/admin/servers/${encodeURIComponent(name)}`,
    );
    return data;
  },

  create: async (input: ServerInput): Promise<Server> => {
    const { data } = await atryumApi.post('/api/v1/admin/servers', input);
    return data;
  },

  update: async (name: string, input: ServerInput): Promise<Server> => {
    const { data } = await atryumApi.put(
      `/api/v1/admin/servers/${encodeURIComponent(name)}`,
      input,
    );
    return data;
  },

  remove: async (name: string, disable = false): Promise<void> => {
    const url = disable
      ? `/api/v1/admin/servers/${encodeURIComponent(name)}?disable=true`
      : `/api/v1/admin/servers/${encodeURIComponent(name)}`;
    await atryumApi.delete(url);
  },

  test: async (name: string): Promise<{ ok: boolean; message: string }> => {
    const { data } = await atryumApi.post(
      `/api/v1/admin/servers/${encodeURIComponent(name)}/test`,
    );
    return data;
  },

  connect: async (name: string): Promise<OAuthConnectStartResponse> => {
    const { data } = await atryumApi.post(
      `/api/v1/admin/servers/${encodeURIComponent(name)}/connect`,
    );
    return data;
  },

  connectStatus: async (name: string): Promise<OAuthConnectStatusResponse> => {
    const { data } = await atryumApi.get(
      `/api/v1/admin/servers/${encodeURIComponent(name)}/connect/status`,
    );
    return data;
  },

  tools: async (name: string): Promise<{ items: ServerTool[] }> => {
    const { data } = await atryumApi.get(
      `/api/v1/admin/servers/${encodeURIComponent(name)}/tools`,
    );
    return data;
  },
};

// ─── Rules ────────────────────────────────────────────────────────────────────

export type RuleAction = 'auto_approve' | 'auto_deny' | 'human_approval' | 'ai_evaluation';

export interface Rule {
  id: string;
  action: RuleAction;
  server_patterns: string[];
  tool_patterns: string[];
  /** Agent CUIDs this rule applies to; empty means all agents. */
  agent_cuids?: string[];
  description?: string;
  /** ValidMind model config CUID used for ai_evaluation rules. */
  model_config_cuid?: string;
  /** Local LLM config ID for native ai_evaluation (alternative to model_config_cuid). */
  atryum_llm_config_id?: string;
  enabled: boolean;
  order: number;
}

export interface RuleInput {
  action: RuleAction;
  server_patterns: string[];
  tool_patterns: string[];
  agent_cuids?: string[];
  description?: string;
  model_config_cuid?: string;
  /** Local LLM config ID for native ai_evaluation (alternative to model_config_cuid). */
  atryum_llm_config_id?: string;
  enabled: boolean;
  /**
   * When present, inserts the new rule before the rule with this ID.
   * Pass an empty string ("") to insert at position 0 (top of list).
   * Omit to append at the end (default).
   */
  insert_before?: string;
}

export const rulesApi = {
  list: async (): Promise<{ items: Rule[] }> => {
    const { data } = await atryumApi.get('/api/v1/admin/rules');
    return data;
  },

  create: async (input: RuleInput): Promise<Rule> => {
    const { data } = await atryumApi.post('/api/v1/admin/rules', input);
    return data;
  },

  update: async (id: string, input: RuleInput): Promise<Rule> => {
    const { data } = await atryumApi.put(
      `/api/v1/admin/rules/${encodeURIComponent(id)}`,
      input,
    );
    return data;
  },

  remove: async (id: string): Promise<void> => {
    await atryumApi.delete(`/api/v1/admin/rules/${encodeURIComponent(id)}`);
  },

  move: async (
    id: string,
    direction: 'up' | 'down',
  ): Promise<{ items: Rule[] }> => {
    const { data } = await atryumApi.put(
      `/api/v1/admin/rules/${encodeURIComponent(id)}/move`,
      { direction },
    );
    return data;
  },
};

// ─── Plans ────────────────────────────────────────────────────────────────────

export type PlanStatus =
  | 'received'
  | 'pending_approval'
  | 'approved'
  | 'denied'
  | 'needs_revision'
  | 'superseded'
  | 'completed'
  | 'expired'
  | 'cancelled';

export interface PlanAction {
  tool: string;
  server?: string;
  description?: string;
  input_summary?: string;
}

export interface Plan {
  plan_id: string;
  agent_id: string;
  source?: string;
  thread_id?: string;
  goal: string;
  rationale?: string;
  actions: PlanAction[];
  status: PlanStatus;
  approval?: InvocationApproval | null;
  matched_rule_id?: string | null;
  feedback?: string;
  parent_plan_id?: string | null;
  revision: number;
  ttl_seconds: number;
  client_name?: string | null;
  client_version?: string | null;
  expires_at?: string | null;
  submitted_at: string;
  decided_at?: string | null;
}

export interface PlanDetail extends Plan {
  revisions: Plan[];
}

export interface PlanFilters {
  status?: string;
  agent_id?: string;
  offset?: number;
  limit?: number;
}

export const plansApi = {
  list: async (
    filters: PlanFilters = {},
  ): Promise<{ items: Plan[]; total: number; offset: number; limit: number }> => {
    const params = new URLSearchParams();
    if (filters.status) params.set('status', filters.status);
    if (filters.agent_id) params.set('agent_id', filters.agent_id);
    if (filters.offset != null) params.set('offset', String(filters.offset));
    params.set('limit', String(filters.limit ?? 50));
    const { data } = await atryumApi.get(`/api/v1/admin/plans?${params.toString()}`);
    return data;
  },

  detail: async (id: string): Promise<PlanDetail> => {
    const { data } = await atryumApi.get(
      `/api/v1/admin/plans/${encodeURIComponent(id)}`,
    );
    return data;
  },

  events: async (id: string): Promise<{ items: InvocationEvent[] }> => {
    const { data } = await atryumApi.get(
      `/api/v1/admin/plans/${encodeURIComponent(id)}/events?limit=200`,
    );
    return data;
  },

  approve: async (id: string, ttlSeconds?: number): Promise<Plan> => {
    const body = ttlSeconds ? { ttl_seconds: ttlSeconds } : {};
    const { data } = await atryumApi.post(
      `/api/v1/admin/plans/${encodeURIComponent(id)}/approve`,
      body,
    );
    return data;
  },

  deny: async (id: string, message?: string): Promise<Plan> => {
    const body = message ? { message } : {};
    const { data } = await atryumApi.post(
      `/api/v1/admin/plans/${encodeURIComponent(id)}/deny`,
      body,
    );
    return data;
  },

  revise: async (id: string, feedback: string): Promise<Plan> => {
    const { data } = await atryumApi.post(
      `/api/v1/admin/plans/${encodeURIComponent(id)}/revise`,
      { feedback },
    );
    return data;
  },

  expire: async (id: string): Promise<Plan> => {
    const { data } = await atryumApi.post(
      `/api/v1/admin/plans/${encodeURIComponent(id)}/expire`,
    );
    return data;
  },
};

// ─── Agents ───────────────────────────────────────────────────────────────────

export interface Agent {
  cuid: string;
  name: string;
  description: string;
  agent_ids: string[];
  claude_managed_agents?: ClaudeManagedAgentBinding[];
  enabled: boolean;
  synced_at: string;
  /** True when this agent originated from a ValidMind sync and cannot be deleted manually. */
  synced: boolean;
  /** Governing text used by local LLM-as-judge evaluation. Only editable for non-synced agents. */
  charter?: string;
  /** Free-form tags. Atryum-native and editable for all agents (including synced ones). */
  tags: string[];
  /** ValidMind inventory model cuid; present only for synced agents. */
  vm_cuid?: string;
}

export interface CharterSegment {
  kind: string;
  header: string;
  text: string;
}

export interface CharterPreview {
  segments: CharterSegment[];
  combined: string;
}

export interface AgentCreateInput {
  name: string;
  description?: string;
  enabled: boolean;
  agent_ids?: string[];
  charter?: string;
  tags?: string[];
  claude_managed_agents?: ClaudeManagedAgentBinding[];
  force_claude_managed_agent_connect?: boolean;
}

export interface AgentUpdateInput {
  name?: string;
  description?: string;
  enabled: boolean;
  agent_ids?: string[];
  charter?: string;
  tags?: string[];
  claude_managed_agents?: ClaudeManagedAgentBinding[];
  force_claude_managed_agent_connect?: boolean;
}

export interface ClaudeManagedAgentBinding {
  id?: string;
  account: string;
  claude_agent_id: string;
  claude_agent_name?: string;
  claude_agent_model?: string;
  claude_agent_version?: number;
}

export interface ClaudeManagedAgent {
  id: string;
  name: string;
  description?: string;
  model?: string;
  version?: number;
  created_at?: string;
  updated_at?: string;
}

export interface ClaudeManagedAgentAccount {
  name: string;
  workspace: string;
}

export interface ManagedAgentSession {
  session_id: string;
  account: string;
  agent_id?: string;
  description?: string;
  last_event_id?: string;
  created_at: string;
  updated_at: string;
}

export const agentsApi = {
  list: async (): Promise<{ items: Agent[] }> => {
    const { data } = await atryumApi.get('/api/v1/admin/agents');
    return data;
  },

  create: async (input: AgentCreateInput): Promise<Agent> => {
    const { data } = await atryumApi.post('/api/v1/admin/agents', input);
    return data;
  },

  update: async (cuid: string, input: AgentUpdateInput): Promise<Agent> => {
    const { data } = await atryumApi.patch(
      `/api/v1/admin/agents/${encodeURIComponent(cuid)}`,
      input,
    );
    return data;
  },

  remove: async (cuid: string): Promise<void> => {
    await atryumApi.delete(
      `/api/v1/admin/agents/${encodeURIComponent(cuid)}`,
    );
  },

  sync: async (): Promise<void> => {
    await atryumApi.post('/api/v1/admin/agents/sync');
  },

  getAgentCharterPreview: async (id: string): Promise<CharterPreview> => {
    const { data } = await atryumApi.get(
      `/api/v1/admin/agents/${encodeURIComponent(id)}/charter-preview`,
    );
    return data;
  },

  managedAgentAccounts: async (): Promise<{ items: ClaudeManagedAgentAccount[] }> => {
    const { data } = await atryumApi.get('/api/v1/admin/managed-agents/accounts');
    return data;
  },

  managedAgentSessions: async (): Promise<{ items: ManagedAgentSession[] }> => {
    const { data } = await atryumApi.get('/api/v1/admin/managed-agents/sessions');
    return data;
  },

  deleteManagedAgentSession: async (sessionID: string): Promise<void> => {
    await atryumApi.delete(
      `/api/v1/admin/managed-agents/sessions/${encodeURIComponent(sessionID)}`,
    );
  },

  clearManagedAgentSessions: async (): Promise<{ deleted: number }> => {
    const { data } = await atryumApi.delete('/api/v1/admin/managed-agents/sessions');
    return data;
  },

  managedAgents: async (
    account?: string,
    q?: string,
  ): Promise<{ items: ClaudeManagedAgent[] }> => {
    const params = new URLSearchParams();
    if (account) params.set('account', account);
    if (q) params.set('q', q);
    const suffix = params.toString() ? `?${params.toString()}` : '';
    const { data } = await atryumApi.get(`/api/v1/admin/managed-agents/agents${suffix}`);
    return data;
  },
};

// ─── Settings ─────────────────────────────────────────────────────────────────

export interface AgentSyncSettings {
  org_cuid: string;
  agent_record_type_slug: string;
  charter_field_key: string;
  summary_model_config_cuid: string;
  summary_atryum_llm_config_id: string;
  backend_configured?: boolean;
  updated_at?: string;
  sync_error?: string;
}

export const settingsApi = {
  get: async (): Promise<AgentSyncSettings> => {
    const { data } = await atryumApi.get('/api/v1/admin/settings');
    return data;
  },

  update: async (
    input: Omit<AgentSyncSettings, 'updated_at' | 'sync_error'>,
  ): Promise<AgentSyncSettings> => {
    const { data } = await atryumApi.put('/api/v1/admin/settings', input);
    return data;
  },
};

// ─── Model Configs ────────────────────────────────────────────────────────────

export interface ModelConfig {
  cuid: string;
  name: string;
}

export const modelConfigsApi = {
  list: async (): Promise<{ items: ModelConfig[]; total: number }> => {
    const { data } = await atryumApi.get('/api/v1/admin/model-configs');
    return data;
  },
};

// ─── VM Discovery ─────────────────────────────────────────────────────────────

export interface VmOrg {
  cuid: string;
  name: string;
}

export interface VmOrgListResponse {
  items: VmOrg[];
  total: number;
  auth_mode?: string;
  single_org?: boolean;
}

export interface VmRecordType {
  cuid: string;
  slug: string;
  name: string;
}

export interface VmCustomField {
  key: string;
  name: string;
  field_type: string;
}

// ─── Local LLM Configs ────────────────────────────────────────────────────────

export type LLMProvider = 'openai' | 'anthropic' | 'openai_compatible';

export interface LLMConfig {
  id: string;
  name: string;
  provider: LLMProvider;
  model: string;
  /** "***" when an API key is stored; empty when not set. */
  api_key?: string;
  base_url?: string;
  enabled: boolean;
  created_at: string;
}

export interface LLMConfigInput {
  name: string;
  provider: LLMProvider;
  model: string;
  api_key?: string;
  base_url?: string;
  enabled?: boolean;
}

export const llmConfigsApi = {
  list: async (): Promise<{ items: LLMConfig[] }> => {
    const { data } = await atryumApi.get('/api/v1/admin/llm-configs');
    return data;
  },

  create: async (input: LLMConfigInput): Promise<LLMConfig> => {
    const { data } = await atryumApi.post('/api/v1/admin/llm-configs', input);
    return data;
  },

  update: async (id: string, input: Partial<LLMConfigInput>): Promise<LLMConfig> => {
    const { data } = await atryumApi.patch(
      `/api/v1/admin/llm-configs/${encodeURIComponent(id)}`,
      input,
    );
    return data;
  },

  remove: async (id: string): Promise<void> => {
    await atryumApi.delete(`/api/v1/admin/llm-configs/${encodeURIComponent(id)}`);
  },
};

// ─── VM Discovery ─────────────────────────────────────────────────────────────

export const vmDiscoveryApi = {
  listOrganizations: async (): Promise<VmOrgListResponse> => {
    const { data } = await atryumApi.get('/api/v1/admin/vm/organizations');
    return data;
  },

  listRecordTypes: async (
    orgCUID: string,
  ): Promise<{ items: VmRecordType[]; total: number }> => {
    const { data } = await atryumApi.get(
      `/api/v1/admin/vm/record-types?org_cuid=${encodeURIComponent(orgCUID)}`,
    );
    return data;
  },

  listCustomFields: async (
    orgCUID: string,
    recordTypeSlug: string,
  ): Promise<{ items: VmCustomField[]; total: number }> => {
    const { data } = await atryumApi.get(
      `/api/v1/admin/vm/custom-fields?org_cuid=${encodeURIComponent(orgCUID)}&record_type_slug=${encodeURIComponent(recordTypeSlug)}`,
    );
    return data;
  },
};
