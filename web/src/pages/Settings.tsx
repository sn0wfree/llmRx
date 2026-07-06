import { useEffect, useState } from 'react';
import { api } from '../api';

const STRATEGIES: { id: string; label: string; desc: string }[] = [
  { id: 'cheapest', label: 'Cheapest', desc: 'Pick the channel with the lowest total price per 1M tokens.' },
  { id: 'fastest', label: 'Fastest', desc: 'Pick the highest-priority channel (priority acts as a latency proxy).' },
  { id: 'balanced', label: 'Balanced', desc: 'Min-max normalize price and priority, then minimize the weighted sum.' },
];

export default function Settings() {
  const [strategy, setStrategy] = useState<string>('cheapest');
  const [saved, setSaved] = useState<string>('cheapest');
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [version, setVersion] = useState<string>('-');

  const reload = async () => {
    try {
      const r = await api.getConfig();
      setStrategy(r.cost_strategy);
      setSaved(r.cost_strategy);
    } catch (e: any) {
      setError(e?.message || 'failed');
    }
  };

  useEffect(() => {
    reload();
    api.dashboard().then((d) => setVersion(`channels=${d.channels_total} tokens=${d.tokens_total}`)).catch(() => {});
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      const r = await api.updateConfig({ cost_strategy: strategy });
      setSaved(r.cost_strategy);
    } catch (e: any) {
      setError(e?.message || 'failed');
    } finally {
      setSaving(false);
    }
  };

  const dirty = strategy !== saved;

  return (
    <div className="max-w-2xl">
      <h1 className="text-2xl font-semibold tracking-tight mb-1">Settings</h1>
      <p className="text-sm text-slate-500 mb-6">
        Runtime configuration. Changes take effect on the next routed request.
      </p>

      {error && (
        <div className="card p-3 mb-4 bg-red-50 border-red-200 text-red-700 text-sm">{error}</div>
      )}

      <div className="card p-5 mb-4">
        <h2 className="text-sm font-semibold text-slate-700 mb-1">L3 Cost Strategy</h2>
        <p className="text-xs text-slate-500 mb-4">
          How the router orders candidate channels after the L1 static match and L2 circuit-breaker filter.
        </p>
        <div className="space-y-2">
          {STRATEGIES.map((s) => {
            const active = strategy === s.id;
            return (
              <label
                key={s.id}
                className={`flex items-start gap-3 p-3 rounded border cursor-pointer ${
                  active
                    ? 'border-brand-500 bg-brand-50/40'
                    : 'border-slate-200 hover:bg-slate-50'
                }`}
              >
                <input
                  type="radio"
                  name="strategy"
                  className="mt-1"
                  checked={active}
                  onChange={() => setStrategy(s.id)}
                />
                <div>
                  <div className="text-sm font-medium text-slate-800">{s.label}</div>
                  <div className="text-xs text-slate-500 mt-0.5">{s.desc}</div>
                </div>
              </label>
            );
          })}
        </div>
        <div className="flex items-center justify-between mt-5">
          <div className="text-xs text-slate-500">
            Active: <span className="font-medium text-slate-700">{saved}</span>
            {dirty && <span className="ml-2 text-amber-600">(unsaved changes)</span>}
          </div>
          <button
            onClick={save}
            disabled={!dirty || saving}
            className={`btn-primary ${(!dirty || saving) ? 'opacity-50 cursor-not-allowed' : ''}`}
          >
            {saving ? 'Saving…' : 'Save'}
          </button>
        </div>
      </div>

      <div className="card p-5">
        <h2 className="text-sm font-semibold text-slate-700 mb-3">System</h2>
        <dl className="text-sm">
          <Row k="Build version" v="llmRx dev" />
          <Row k="Runtime" v={version} />
        </dl>
      </div>
    </div>
  );
}

function Row({ k, v }: { k: string; v: string }) {
  return (
    <div className="flex py-1.5 border-b border-slate-100 last:border-0">
      <dt className="w-40 text-slate-500">{k}</dt>
      <dd className="text-slate-800">{v}</dd>
    </div>
  );
}
