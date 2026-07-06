import { useEffect, useState } from 'react';
import { api, LogEntry } from '../api';

export default function Logs() {
  const [items, setItems] = useState<LogEntry[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState('');
  const [autoRefresh, setAutoRefresh] = useState(true);

  const reload = async () => {
    try {
      const d = await api.listLogs(100, 0);
      setItems(d.data);
    } catch (e: any) {
      setError(e?.message || 'failed');
    }
  };

  useEffect(() => {
    reload();
    if (!autoRefresh) return;
    const id = setInterval(reload, 5000);
    return () => clearInterval(id);
  }, [autoRefresh]);

  const filtered = filter
    ? items.filter((l) =>
        [l.model, l.router_path, String(l.status_code), String(l.channel_id), String(l.token_id)]
          .join(' ')
          .toLowerCase()
          .includes(filter.toLowerCase())
      )
    : items;

  return (
    <div>
      <div className="flex items-end justify-between mb-6">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Request Logs</h1>
          <p className="text-sm text-slate-500 mt-1">
            Last 100 requests, newest first.
          </p>
        </div>
        <div className="flex items-center gap-3">
          <label className="text-sm flex items-center gap-2">
            <input
              type="checkbox"
              checked={autoRefresh}
              onChange={(e) => setAutoRefresh(e.target.checked)}
            />
            Auto-refresh
          </label>
          <input
            className="input w-64"
            placeholder="Filter…"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          />
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
            {filtered.map((l) => (
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
            {filtered.length === 0 && (
              <tr>
                <td className="px-3 py-6 text-center text-slate-400" colSpan={10}>
                  {items.length === 0 ? 'No logs yet.' : 'No matches.'}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}