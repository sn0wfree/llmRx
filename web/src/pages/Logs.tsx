import { useEffect, useRef, useState } from 'react';
import { api, getSessionToken, LogEntry, Channel, Token } from '../api';

export default function Logs() {
  const [items, setItems] = useState<LogEntry[]>([]);
  const [total, setTotal] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [autoRefresh, setAutoRefresh] = useState(true);
  const [live, setLive] = useState(false);
  const [liveStatus, setLiveStatus] = useState<'off' | 'connecting' | 'live' | 'error'>('off');

  const [channels, setChannels] = useState<Channel[]>([]);
  const [tokens, setTokens] = useState<Token[]>([]);

  const [f, setF] = useState<{
    token_id: string;
    channel_id: string;
    model: string;
    status_code: string;
    from: string;
    to: string;
  }>({
    token_id: '',
    channel_id: '',
    model: '',
    status_code: '',
    from: '',
    to: '',
  });

  const reload = async () => {
    try {
      const r = await api.listLogs(200, 0, {
        token_id: f.token_id ? Number(f.token_id) : undefined,
        channel_id: f.channel_id ? Number(f.channel_id) : undefined,
        model: f.model || undefined,
        status_code: f.status_code ? Number(f.status_code) : undefined,
        from: f.from || undefined,
        to: f.to || undefined,
      });
      setItems(r.data);
      setTotal(r.total);
    } catch (e: any) {
      setError(e?.message || 'failed');
    }
  };

  useEffect(() => {
    api.listChannelsForFilter().then((r) => setChannels(r.data)).catch(() => {});
    api.listTokensForFilter().then((r) => setTokens(r.data)).catch(() => {});
  }, []);

  useEffect(() => {
    if (live) return; // SSE drives updates
    reload();
    if (!autoRefresh) return;
    const id = setInterval(reload, 5000);
    return () => clearInterval(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoRefresh, f, live]);

  // SSE live tailing.
  const esRef = useRef<EventSource | null>(null);
  useEffect(() => {
    if (!live) {
      esRef.current?.close();
      esRef.current = null;
      setLiveStatus('off');
      return;
    }
    const tok = getSessionToken();
    if (!tok) {
      setError('no session');
      setLive(false);
      return;
    }
    setLiveStatus('connecting');
    const es = new EventSource(`/api/v1/logs/stream?session_token=${encodeURIComponent(tok)}`);
    esRef.current = es;
    es.addEventListener('open', () => setLiveStatus('live'));
    es.addEventListener('log', (ev) => {
      try {
        const entry = JSON.parse((ev as MessageEvent).data) as LogEntry;
        setItems((prev) => [entry, ...prev].slice(0, 500));
        setTotal((t) => t + 1);
      } catch {
        // ignore parse error
      }
    });
    es.addEventListener('error', () => {
      setLiveStatus('error');
      es.close();
      esRef.current = null;
    });
    return () => {
      es.close();
      esRef.current = null;
    };
  }, [live]);

  const reset = () =>
    setF({ token_id: '', channel_id: '', model: '', status_code: '', from: '', to: '' });

  return (
    <div>
      <div className="flex items-end justify-between mb-6">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Request Logs</h1>
          <p className="text-sm text-slate-500 mt-1">
            {total.toLocaleString()} matching request{total === 1 ? '' : 's'}.
            {live && liveStatus === 'live' && (
              <span className="ml-2 inline-flex items-center gap-1 text-green-700">
                <span className="w-1.5 h-1.5 rounded-full bg-green-500 animate-pulse" />
                live
              </span>
            )}
            {live && liveStatus === 'connecting' && (
              <span className="ml-2 text-slate-400">connecting…</span>
            )}
            {live && liveStatus === 'error' && (
              <span className="ml-2 text-red-600">stream error (retrying…)</span>
            )}
          </p>
        </div>
        <div className="flex items-center gap-4">
          <label className="text-sm flex items-center gap-2">
            <input
              type="checkbox"
              checked={live}
              onChange={(e) => setLive(e.target.checked)}
            />
            Live
          </label>
          {!live && (
            <label className="text-sm flex items-center gap-2">
              <input
                type="checkbox"
                checked={autoRefresh}
                onChange={(e) => setAutoRefresh(e.target.checked)}
              />
              Auto-refresh
            </label>
          )}
        </div>
      </div>

      <div className="card p-4 mb-4">
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-6 gap-3">
          <Field label="Token">
            <select
              className="input"
              value={f.token_id}
              onChange={(e) => setF({ ...f, token_id: e.target.value })}
            >
              <option value="">All</option>
              {tokens.map((t) => (
                <option key={t.id} value={t.id}>
                  #{t.id} {t.name}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Channel">
            <select
              className="input"
              value={f.channel_id}
              onChange={(e) => setF({ ...f, channel_id: e.target.value })}
            >
              <option value="">All</option>
              {channels.map((c) => (
                <option key={c.id} value={c.id}>
                  #{c.id} {c.name}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Model">
            <input
              className="input"
              value={f.model}
              onChange={(e) => setF({ ...f, model: e.target.value })}
              placeholder="e.g. gpt-4"
            />
          </Field>
          <Field label="Status">
            <select
              className="input"
              value={f.status_code}
              onChange={(e) => setF({ ...f, status_code: e.target.value })}
            >
              <option value="">Any</option>
              <option value="200">200 OK</option>
              <option value="400">400</option>
              <option value="401">401</option>
              <option value="403">403</option>
              <option value="429">429</option>
              <option value="500">5xx</option>
            </select>
          </Field>
          <Field label="From">
            <input
              type="datetime-local"
              className="input"
              value={f.from ? toLocal(f.from) : ''}
              onChange={(e) => setF({ ...f, from: e.target.value ? fromLocal(e.target.value) : '' })}
            />
          </Field>
          <Field label="To">
            <input
              type="datetime-local"
              className="input"
              value={f.to ? toLocal(f.to) : ''}
              onChange={(e) => setF({ ...f, to: e.target.value ? fromLocal(e.target.value) : '' })}
            />
          </Field>
        </div>
        <div className="flex items-center justify-between mt-3">
          <div className="text-xs text-slate-500">
            Showing {items.length} of {total.toLocaleString()} (max 200 per page)
          </div>
          <button onClick={reset} className="btn-ghost text-xs">
            Clear filters
          </button>
        </div>
      </div>

      {error && (
        <div className="card p-3 mb-4 bg-red-50 border-red-200 text-red-700 text-sm">{error}</div>
      )}

      <div className="card overflow-hidden">
        <table className="w-full text-sm">
          <thead className="bg-slate-50 text-left">
            <tr>
              <th className="px-3 py-2 font-medium text-slate-600">Time</th>
              <th className="px-3 py-2 font-medium text-slate-600">Token</th>
              <th className="px-3 py-2 font-medium text-slate-600">Channel</th>
              <th className="px-3 py-2 font-medium text-slate-600">Model</th>
              <th className="px-3 py-2 font-medium text-slate-600 text-right">Prompt</th>
              <th className="px-3 py-2 font-medium text-slate-600 text-right">Completion</th>
              <th className="px-3 py-2 font-medium text-slate-600 text-right">Cost</th>
              <th className="px-3 py-2 font-medium text-slate-600 text-right">Ms</th>
              <th className="px-3 py-2 font-medium text-slate-600">Code</th>
              <th className="px-3 py-2 font-medium text-slate-600">Path</th>
            </tr>
          </thead>
          <tbody>
            {items.map((l) => (
              <tr key={l.id} className="border-t border-slate-100">
                <td className="px-3 py-2 text-slate-500 whitespace-nowrap">
                  {l.created_at.replace('T', ' ').slice(0, 19)}
                </td>
                <td className="px-3 py-2 text-slate-500">{l.token_id || '—'}</td>
                <td className="px-3 py-2 text-slate-500">{l.channel_id}</td>
                <td className="px-3 py-2 font-medium">{l.model || '—'}</td>
                <td className="px-3 py-2 text-slate-600 text-right">{l.prompt_tokens}</td>
                <td className="px-3 py-2 text-slate-600 text-right">{l.completion_tokens}</td>
                <td className="px-3 py-2 text-slate-600 text-right">
                  ${l.real_cost_usd.toFixed(6)}
                </td>
                <td className="px-3 py-2 text-slate-600 text-right">{l.duration_ms}</td>
                <td className="px-3 py-2">
                  <span
                    className={`badge ${
                      l.status_code >= 400
                        ? 'bg-red-50 text-red-700 border border-red-200'
                        : 'bg-green-50 text-green-700 border border-green-200'
                    }`}
                  >
                    {l.status_code}
                  </span>
                </td>
                <td className="px-3 py-2 text-xs text-slate-500 truncate max-w-xs" title={l.router_path}>
                  {l.router_path}
                </td>
              </tr>
            ))}
            {items.length === 0 && (
              <tr>
                <td className="px-3 py-6 text-center text-slate-400" colSpan={10}>
                  No logs match the current filter.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block text-xs">
      <span className="text-slate-500 uppercase tracking-wide">{label}</span>
      <div className="mt-1">{children}</div>
    </label>
  );
}

// Convert between RFC3339 and <input type=datetime-local> (which uses
// local time, no Z suffix).
function toLocal(rfc: string): string {
  const d = new Date(rfc);
  const off = d.getTimezoneOffset() * 60_000;
  return new Date(d.getTime() - off).toISOString().slice(0, 16);
}
function fromLocal(s: string): string {
  return new Date(s).toISOString();
}
