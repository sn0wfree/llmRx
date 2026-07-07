import { FormEvent, useEffect, useState } from 'react';
import { api, Channel } from '../api';

interface ChannelForm {
  name: string;
  provider: string;
  base_url: string;
  models: string;
  priority: number;
  input_price_per_1m: number;
  output_price_per_1m: number;
  max_failures: number;
  reset_timeout_ms: number;
  cached_input_discount: number;
}

const EMPTY: ChannelForm = {
  name: '',
  provider: 'openai',
  base_url: 'https://api.openai.com/v1',
  models: '',
  priority: 1,
  input_price_per_1m: 0,
  output_price_per_1m: 0,
  max_failures: 5,
  reset_timeout_ms: 60000,
  cached_input_discount: 0.1,
};

export default function Channels() {
  const [items, setItems] = useState<Channel[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState<ChannelForm>(EMPTY);

  const reload = async () => {
    try {
      const d = await api.listChannels();
      setItems(d.data);
    } catch (e: any) {
      setError(e?.message || 'failed');
    }
  };

  useEffect(() => {
    reload();
  }, []);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    try {
      await api.createChannel({
        name: form.name,
        provider: form.provider,
        base_url: form.base_url,
        models: form.models.split(',').map((s) => s.trim()).filter(Boolean),
        priority: form.priority,
        input_price_per_1m: form.input_price_per_1m,
        output_price_per_1m: form.output_price_per_1m,
        cached_input_discount: form.cached_input_discount,
        status: 1,
        circuit_breaker: {
          max_failures: form.max_failures,
          reset_timeout: form.reset_timeout_ms * 1_000_000,
        },
      });
      setCreating(false);
      setForm(EMPTY);
      reload();
    } catch (e: any) {
      setError(e?.message || 'create failed');
    }
  };

  const toggle = async (ch: Channel) => {
    const next: Channel = { ...ch, status: ch.status === 1 ? 2 : 1 };
    try {
      await api.updateChannel(ch.id, next);
      reload();
    } catch (e: any) {
      setError(e?.message || 'update failed');
    }
  };

  const remove = async (ch: Channel) => {
    if (!confirm(`Delete channel "${ch.name}" and all its keys?`)) return;
    try {
      await api.deleteChannel(ch.id);
      reload();
    } catch (e: any) {
      setError(e?.message || 'delete failed');
    }
  };

  return (
    <div>
      <div className="flex items-end justify-between mb-6">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Channels</h1>
          <p className="text-sm text-slate-500 mt-1">
            Upstream providers. Status 1 = enabled, 2 = disabled.
          </p>
        </div>
        <button className="btn-primary" onClick={() => setCreating(!creating)}>
          {creating ? 'Cancel' : '+ New channel'}
        </button>
      </div>

      {error && (
        <div className="card p-3 mb-4 bg-red-50 border-red-200 text-red-700 text-sm">{error}</div>
      )}

      {creating && (
        <form onSubmit={submit} className="card p-6 mb-6">
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div>
              <label className="label">Name</label>
              <input className="input" required value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
            </div>
            <div>
              <label className="label">Provider</label>
              <input className="input" required value={form.provider} onChange={(e) => setForm({ ...form, provider: e.target.value })} />
            </div>
            <div className="md:col-span-2">
              <label className="label">Base URL</label>
              <input className="input" required value={form.base_url} onChange={(e) => setForm({ ...form, base_url: e.target.value })} />
            </div>
            <div className="md:col-span-2">
              <label className="label">Models (comma-separated)</label>
              <input className="input" required value={form.models} onChange={(e) => setForm({ ...form, models: e.target.value })} />
            </div>
            <div>
              <label className="label">Priority</label>
              <input className="input" type="number" value={form.priority} onChange={(e) => setForm({ ...form, priority: parseInt(e.target.value, 10) || 0 })} />
            </div>
            <div>
              <label className="label">Max failures</label>
              <input className="input" type="number" value={form.max_failures} onChange={(e) => setForm({ ...form, max_failures: parseInt(e.target.value, 10) || 0 })} />
            </div>
            <div>
              <label className="label">Input $ / 1M tokens</label>
              <input className="input" type="number" step="0.0001" value={form.input_price_per_1m} onChange={(e) => setForm({ ...form, input_price_per_1m: parseFloat(e.target.value) || 0 })} />
            </div>
            <div>
              <label className="label">Output $ / 1M tokens</label>
              <input className="input" type="number" step="0.0001" value={form.output_price_per_1m} onChange={(e) => setForm({ ...form, output_price_per_1m: parseFloat(e.target.value) || 0 })} />
            </div>
            <div>
              <label className="label">Reset timeout (ms)</label>
              <input className="input" type="number" value={form.reset_timeout_ms} onChange={(e) => setForm({ ...form, reset_timeout_ms: parseInt(e.target.value, 10) || 0 })} />
            </div>
            <div>
              <label className="label">Cached input discount</label>
              <input className="input" type="number" step="0.01" min="0" max="1" value={form.cached_input_discount} onChange={(e) => setForm({ ...form, cached_input_discount: parseFloat(e.target.value) || 0 })} />
              <p className="text-xs text-slate-500 mt-1">Fraction of input price charged for cached prompt tokens (0 = no discount, 0.1 = Anthropic default, 1 = free).</p>
            </div>
          </div>
          <div className="mt-6 flex gap-2">
            <button className="btn-primary" type="submit">Create</button>
            <button className="btn-ghost" type="button" onClick={() => setCreating(false)}>Cancel</button>
          </div>
        </form>
      )}

      <div className="card overflow-hidden">
        <table className="w-full text-sm">
          <thead className="bg-slate-50 text-left">
            <tr>
              <th className="px-4 py-2 font-medium text-slate-600">ID</th>
              <th className="px-4 py-2 font-medium text-slate-600">Name</th>
              <th className="px-4 py-2 font-medium text-slate-600">Provider</th>
              <th className="px-4 py-2 font-medium text-slate-600">Models</th>
              <th className="px-4 py-2 font-medium text-slate-600">Priority</th>
              <th className="px-4 py-2 font-medium text-slate-600">Status</th>
              <th className="px-4 py-2 font-medium text-slate-600">Actions</th>
            </tr>
          </thead>
          <tbody>
            {items.map((ch) => (
              <tr key={ch.id} className="border-t border-slate-100">
                <td className="px-4 py-2 text-slate-500">{ch.id}</td>
                <td className="px-4 py-2 font-medium">{ch.name}</td>
                <td className="px-4 py-2">{ch.provider}</td>
                <td className="px-4 py-2 text-slate-600">
                  {ch.models.map((m) => (
                    <span key={m} className="badge bg-slate-100 text-slate-700 mr-1 mb-1 inline-block">
                      {m}
                    </span>
                  ))}
                </td>
                <td className="px-4 py-2">{ch.priority}</td>
                <td className="px-4 py-2">
                  <span
                    className={`badge ${
                      ch.status === 1
                        ? 'bg-green-50 text-green-700 border border-green-200'
                        : 'bg-slate-100 text-slate-600 border border-slate-200'
                    }`}
                  >
                    {ch.status === 1 ? 'enabled' : 'disabled'}
                  </span>
                </td>
                <td className="px-4 py-2">
                  <button className="btn-ghost mr-1" onClick={() => toggle(ch)}>
                    {ch.status === 1 ? 'Disable' : 'Enable'}
                  </button>
                  <button className="btn-danger" onClick={() => remove(ch)}>
                    Delete
                  </button>
                </td>
              </tr>
            ))}
            {items.length === 0 && (
              <tr>
                <td className="px-4 py-6 text-center text-slate-400" colSpan={7}>
                  No channels yet.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}