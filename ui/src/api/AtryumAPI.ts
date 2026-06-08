import axios from 'axios';

export const atryumApi = axios.create({
  headers: { 'Content-Type': 'application/json' },
});

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
  ): Promise<{ items: Invocation[] }> => {
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

export type RuleAction = 'auto_approve' | 'auto_deny' | 'human_approval';

export interface Rule {
  id: string;
  action: RuleAction;
  server_patterns: string[];
  tool_patterns: string[];
  user_pattern: string;
  /** Agent CUIDs this rule applies to; empty means all agents. */
  agent_cuids?: string[];
  description?: string;
  enabled: boolean;
  order: number;
}

export interface RuleInput {
  action: RuleAction;
  server_patterns: string[];
  tool_patterns: string[];
  user_pattern: string;
  agent_cuids?: string[];
  description?: string;
  enabled: boolean;
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

// ─── Agents ───────────────────────────────────────────────────────────────────

export interface Agent {
  cuid: string;
  name: string;
  description: string;
  agent_ids: string[];
  enabled: boolean;
  synced_at: string;
}

export interface AgentCreateInput {
  name: string;
  description?: string;
  enabled: boolean;
  agent_ids?: string[];
}

export interface AgentUpdateInput {
  name?: string;
  description?: string;
  enabled: boolean;
  agent_ids?: string[];
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
};
