import { FormEvent, useEffect, useState } from 'react';
import { api, Token } from '../api';

export default function Tokens() {
  const [items, setItems] = useState<Token[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState('');
  const [expires, setExpires] = useState(30);
  const [rpm, setRpm] = useState(0);
  const [models, setModels] = useState('');
  const [issuedKey, setIssuedKey] = useState<{ key: string; name: string } | null>(null);

  const reload = async () => {
    try {
      const d = await api.listTokens();
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
      const res = await api.createToken({
        name,
        expires_in_days: expires || undefined,
        rpm: rpm || undefined,
        models_whitelist: models.split(',').map((s) => s.trim()).filter(Boolean),
      });
      setIssuedKey({ key: res.key, name: res.name });
      setCreating(false);
      setName('');
      setModels('');
      reload();
    } catch (e: any) {
      setError(e?.message || 'create failed');
    }
  };

  const remove = async (t: Token) => {
    if (!confirm(`Revoke token "${t.name || t.id}"?`)) return;
    try {
      await api.deleteToken(t.id);
      reload();
    } catch (e: any) {
      setError(e?.message || 'delete failed');
    }
  };

  return (
    <div>
      <div className="flex items-end justify-between mb-6">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">API Tokens</h1>
          <p className="text-sm text-slate-500 mt-1">
            Virtual keys for client apps. Status 0 = active.
          </p>
        </div>
        <button className="btn-primary" onClick={() => setCreating(!creating)}>
          {creating ? 'Cancel' : '+ New token'}
        </button>
      </div>

      {error && (
        <div className="card p-3 mb-4 bg-red-50 border-red-200 text-red-700 text-sm">{error}</div>
      )}

      {issuedKey && (
        <div className="card p-4 mb-4 border-amber-200 bg-amber-50">
          <div className="text-sm font-medium text-amber-900">
            New token "{issuedKey.name}" issued — copy now, shown only once:
          </div>
          <code className="block mt-2 p-2 bg-white rounded border border-amber-200 text-sm font-mono break-all">
            {issuedKey.key}
          </code>
          <button className="btn-ghost mt-3" onClick={() => setIssuedKey(null)}>Dismiss</button>
        </div>
      )}

      {creating && (
        <form onSubmit={submit} className="card p-6 mb-6">
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div>
              <label className="label">Name</label>
              <input className="input" required value={name} onChange={(e) => setName(e.target.value)} />
            </div>
            <div>
              <label className="label">Expires in (days)</label>
              <input className="input" type="number" min="0" value={expires} onChange={(e) => setExpires(parseInt(e.target.value, 10) || 0)} />
            </div>
            <div>
              <label className="label">RPM limit (0 = unlimited)</label>
              <input className="input" type="number" min="0" value={rpm} onChange={(e) => setRpm(parseInt(e.target.value, 10) || 0)} />
            </div>
            <div>
              <label className="label">Models whitelist (empty = all)</label>
              <input className="input" value={models} onChange={(e) => setModels(e.target.value)} placeholder="deepseek-chat, deepseek-reasoner" />
            </div>
          </div>
          <div className="mt-6 flex gap-2">
            <button className="btn-primary" type="submit">Create token</button>
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
              <th className="px-4 py-2 font-medium text-slate-600">Status</th>
              <th className="px-4 py-2 font-medium text-slate-600">RPM</th>
              <th className="px-4 py-2 font-medium text-slate-600">Whitelist</th>
              <th className="px-4 py-2 font-medium text-slate-600">Expires</th>
              <th className="px-4 py-2 font-medium text-slate-600">Created</th>
              <th className="px-4 py-2 font-medium text-slate-600"></th>
            </tr>
          </thead>
          <tbody>
            {items.map((t) => (
              <tr key={t.id} className="border-t border-slate-100">
                <td className="px-4 py-2 text-slate-500">{t.id}</td>
                <td className="px-4 py-2 font-medium">{t.name || '(unnamed)'}</td>
                <td className="px-4 py-2">
                  <span
                    className={`badge ${
                      t.status === 0
                        ? 'bg-green-50 text-green-700 border border-green-200'
                        : 'bg-slate-100 text-slate-600 border border-slate-200'
                    }`}
                  >
                    {t.status === 0 ? 'active' : 'revoked'}
                  </span>
                </td>
                <td className="px-4 py-2 text-slate-600">{t.rpm || '∞'}</td>
                <td className="px-4 py-2 text-slate-600">
                  {t.models_whitelist.length === 0 ? 'all' : t.models_whitelist.join(', ')}
                </td>
                <td className="px-4 py-2 text-slate-600">
                  {t.expires_at && t.expires_at.startsWith('0001') ? '—' : t.expires_at.slice(0, 10)}
                </td>
                <td className="px-4 py-2 text-slate-500">{t.created_at.slice(0, 10)}</td>
                <td className="px-4 py-2 text-right">
                  {t.status === 0 && (
                    <button className="btn-danger" onClick={() => remove(t)}>
                      Revoke
                    </button>
                  )}
                </td>
              </tr>
            ))}
            {items.length === 0 && (
              <tr>
                <td className="px-4 py-6 text-center text-slate-400" colSpan={8}>
                  No tokens yet.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}