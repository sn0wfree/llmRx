import { FormEvent, useState } from 'react';
import { api, setSessionToken } from '../api';

export default function Login({ onSuccess }: { onSuccess: () => void }) {
  const [username, setUsername] = useState('admin');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      const res = await api.login(username, password);
      setSessionToken(res.session_token);
      onSuccess();
    } catch (err: any) {
      setError(err?.message || 'login failed');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="min-h-screen flex items-center justify-center bg-gradient-to-br from-slate-900 to-slate-700">
      <form onSubmit={submit} className="card w-96 p-8 shadow-xl">
        <div className="mb-6 text-center">
          <div className="text-2xl font-semibold tracking-tight text-slate-900">llmRx</div>
          <div className="text-sm text-slate-500 mt-1">Admin Console</div>
        </div>

        <div className="space-y-4">
          <div>
            <label className="label">Username</label>
            <input
              className="input"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoFocus
              required
            />
          </div>
          <div>
            <label className="label">Password</label>
            <input
              className="input"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
            />
          </div>
        </div>

        {error && (
          <div className="mt-4 rounded-md bg-red-50 border border-red-200 px-3 py-2 text-sm text-red-700">
            {error}
          </div>
        )}

        <button type="submit" disabled={busy} className="btn-primary w-full mt-6">
          {busy ? 'Signing in…' : 'Sign in'}
        </button>

        <div className="text-xs text-slate-400 mt-6 text-center">
          Default credentials: admin / admin (override via server.admin_password)
        </div>
      </form>
    </div>
  );
}