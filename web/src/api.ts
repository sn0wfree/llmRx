const TOKEN_KEY = 'llmrx_session';

export function getSessionToken(): string | null {
  return localStorage.getItem(TOKEN_KEY);
}

export function setSessionToken(t: string | null) {
  if (t === null) {
    localStorage.removeItem(TOKEN_KEY);
  } else {
    localStorage.setItem(TOKEN_KEY, t);
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  const tok = getSessionToken();
  if (tok) {
    headers['X-Session-Token'] = tok;
  }
  const res = await fetch(`/api/v1${path}`, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
  });
  if (res.status === 401) {
    setSessionToken(null);
    window.location.hash = '#/login';
    throw new Error('unauthorized');
  }
  if (!res.ok) {
    const text = await res.text();
    let msg = text;
    try {
      const j = JSON.parse(text);
      msg = j.error?.message || text;
    } catch {}
    throw new Error(msg || `HTTP ${res.status}`);
  }
  if (res.status === 204) return undefined as unknown as T;
  return res.json();
}

export const api = {
  login: (username: string, password: string) =>
    request<{ session_token: string; user: { id: number; username: string; role: number } }>(
      'POST',
      '/login',
      { username, password }
    ),
  logout: () => request<{ ok: boolean }>('POST', '/logout'),

  dashboard: () =>
    request<{
      channels_total: number;
      channels_active: number;
      tokens_total: number;
      tokens_active: number;
      keys_by_channel: Record<string, number>;
      logs_total: number;
      logs_errors: number;
      prompt_tokens: number;
      completion_tokens: number;
      real_cost_usd: number;
      billed_cost_usd: number;
    }>('GET', '/dashboard'),

  listChannels: () => request<{ data: Channel[] }>('GET', '/channels'),
  createChannel: (ch: Partial<Channel>) => request<Channel>('POST', '/channels', ch),
  updateChannel: (id: number, ch: Partial<Channel>) =>
    request<Channel>('PUT', `/channels/${id}`, ch),
  deleteChannel: (id: number) => request<{ ok: boolean }>('DELETE', `/channels/${id}`),

  listKeys: (channelId: number) =>
    request<{ data: ApiKey[] }>('GET', `/channels/${channelId}/keys`),
  createKey: (channelId: number, key: string) =>
    request<ApiKey>('POST', `/channels/${channelId}/keys`, { key }),
  deleteKey: (channelId: number, keyId: number) =>
    request<{ ok: boolean }>('DELETE', `/channels/${channelId}/keys/${keyId}`),

  listTokens: () => request<{ data: Token[] }>('GET', '/tokens'),
  createToken: (body: {
    name: string;
    expires_in_days?: number;
    rpm?: number;
    tpm?: number;
    models_whitelist?: string[];
  }) =>
    request<{ id: number; key: string; name: string }>('POST', '/tokens', body),
  deleteToken: (id: number) => request<{ ok: boolean }>('DELETE', `/tokens/${id}`),

  listLogs: (
    limit = 50,
    offset = 0,
    f: {
      token_id?: number;
      channel_id?: number;
      model?: string;
      status_code?: number;
      from?: string;
      to?: string;
    } = {}
  ) => {
    const qs = new URLSearchParams({ limit: String(limit), offset: String(offset) });
    if (f.token_id) qs.set('token_id', String(f.token_id));
    if (f.channel_id) qs.set('channel_id', String(f.channel_id));
    if (f.model) qs.set('model', f.model);
    if (f.status_code) qs.set('status_code', String(f.status_code));
    if (f.from) qs.set('from', f.from);
    if (f.to) qs.set('to', f.to);
    return request<{ data: LogEntry[]; total: number; limit: number; offset: number }>(
      'GET',
      `/logs?${qs.toString()}`
    );
  },

  analyticsTimeSeries: (bucketSec = 3600, f: { from?: string; to?: string; token_id?: number; channel_id?: number } = {}) => {
    const qs = new URLSearchParams({ bucket: String(bucketSec) });
    if (f.from) qs.set('from', f.from);
    if (f.to) qs.set('to', f.to);
    if (f.token_id) qs.set('token_id', String(f.token_id));
    if (f.channel_id) qs.set('channel_id', String(f.channel_id));
    return request<{ data: SeriesPoint[]; bucket: number }>('GET', `/analytics/timeseries?${qs.toString()}`);
  },
  analyticsByModel: (limit = 10, f: { from?: string; to?: string } = {}) => {
    const qs = new URLSearchParams({ limit: String(limit) });
    if (f.from) qs.set('from', f.from);
    if (f.to) qs.set('to', f.to);
    return request<{ data: NamedMetric[] }>('GET', `/analytics/by-model?${qs.toString()}`);
  },
  analyticsByChannel: (limit = 10, f: { from?: string; to?: string } = {}) => {
    const qs = new URLSearchParams({ limit: String(limit) });
    if (f.from) qs.set('from', f.from);
    if (f.to) qs.set('to', f.to);
    return request<{ data: NamedMetric[] }>('GET', `/analytics/by-channel?${qs.toString()}`);
  },
  analyticsByToken: (limit = 10, f: { from?: string; to?: string } = {}) => {
    const qs = new URLSearchParams({ limit: String(limit) });
    if (f.from) qs.set('from', f.from);
    if (f.to) qs.set('to', f.to);
    return request<{ data: NamedMetric[] }>('GET', `/analytics/by-token?${qs.toString()}`);
  },

  listChannelsForFilter: () => request<{ data: Channel[] }>('GET', '/channels'),
  listTokensForFilter: () => request<{ data: Token[] }>('GET', '/tokens'),

  getConfig: () =>
    request<{
      cost_strategy: string;
      breaker_max_failures: number;
      breaker_reset_timeout_ms: number;
      alert_cooldown_sec: number;
      log_retention_days: number;
      markup_ratio: number;
    }>('GET', '/config'),
  updateConfig: (body: {
    cost_strategy?: string;
    breaker_max_failures?: number;
    breaker_reset_timeout_ms?: number;
    alert_cooldown_sec?: number;
    log_retention_days?: number;
    markup_ratio?: number;
  }) =>
    request<{
      cost_strategy: string;
      breaker_max_failures: number;
      breaker_reset_timeout_ms: number;
      alert_cooldown_sec: number;
      log_retention_days: number;
      markup_ratio: number;
    }>('PUT', '/config', body),

  changePassword: (userId: number, body: { old_password?: string; new_password: string }) =>
    request<{ ok: boolean }>('POST', `/users/${userId}/password`, body),

  listAlerts: () =>
    request<{ data: AlertConfig[] }>('GET', '/alerts'),
  createAlert: (a: Partial<AlertConfig>) => request<AlertConfig>('POST', '/alerts', a),
  updateAlert: (id: number, a: Partial<AlertConfig>) =>
    request<AlertConfig>('PUT', `/alerts/${id}`, a),
  deleteAlert: (id: number) => request<{ ok: boolean }>('DELETE', `/alerts/${id}`),
  listAlertEvents: (limit = 100) =>
    request<{ data: AlertEvent[] }>('GET', `/alerts/events?limit=${limit}`),
  ackAlertEvent: (id: number) =>
    request<{ ok: boolean }>('POST', `/alerts/events/${id}/ack`),
};

export interface Channel {
  id: number;
  name: string;
  provider: string;
  base_url: string;
  models: string[];
  priority: number;
  input_price_per_1m: number;
  output_price_per_1m: number;
  cached_input_discount: number;
  status: number;
  circuit_breaker: { max_failures: number; reset_timeout: number };
}

export interface ApiKey {
  id: number;
  channel_id: number;
  key_masked: string;
  status: number;
}

export interface Token {
  id: number;
  plan_id: number;
  name: string;
  status: number;
  rpm: number;
  tpm: number;
  models_whitelist: string[];
  ip_whitelist: string[];
  expires_at: string;
  last_used_at: string;
  created_at: string;
}

export interface LogEntry {
  id: number;
  token_id: number;
  channel_id: number;
  key_id: number;
  model: string;
  prompt_tokens: number;
  completion_tokens: number;
  real_cost_usd: number;
  billed_cost_usd: number;
  duration_ms: number;
  status_code: number;
  router_path: string;
  request_ip: string;
  created_at: string;
}

export interface SeriesPoint {
  bucket: number;
  requests: number;
  errors: number;
  prompt_tokens: number;
  completion_tokens: number;
  real_cost_usd: number;
  billed_cost_usd: number;
}

export interface NamedMetric {
  label: string;
  count: number;
  tokens: number;
  cost: number;
}

export interface AlertConfig {
  id: number;
  name: string;
  type: 'error_rate' | 'p95_latency' | 'cost_spike' | 'key_exhausted';
  threshold: number;
  window_sec: number;
  cooldown_sec: number;
  webhook_url: string;
  enabled: boolean;
  last_fired_at: number;
  created_at: string;
}

export interface AlertEvent {
  id: number;
  alert_id: number;
  alert_name: string;
  alert_type: string;
  fired_at: string;
  payload: string;
  delivered_webhook: boolean;
  acknowledged: boolean;
}