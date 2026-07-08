import { useEffect, useState } from 'react';
import { api } from '../api';

type Tab = 'routing' | 'security' | 'alerts' | 'maintenance';

export default function Settings() {
  const [tab, setTab] = useState<Tab>('routing');

  return (
    <div className="max-w-3xl">
      <h1 className="text-2xl font-semibold tracking-tight mb-1">Settings</h1>
      <p className="text-sm text-slate-500 mb-6">
        Runtime configuration. Changes take effect on the next routed request.
      </p>

      <div className="border-b border-slate-200 mb-6">
        <nav className="flex gap-6">
          {(['routing', 'security', 'alerts', 'maintenance'] as Tab[]).map((t) => (
            <button
              key={t}
              onClick={() => setTab(t)}
              className={`pb-2 text-sm font-medium border-b-2 ${
                tab === t
                  ? 'border-brand-500 text-brand-700'
                  : 'border-transparent text-slate-500 hover:text-slate-700'
              }`}
            >
              {t.charAt(0).toUpperCase() + t.slice(1)}
            </button>
          ))}
        </nav>
      </div>

      {tab === 'routing' && <RoutingTab />}
      {tab === 'security' && <SecurityTab />}
      {tab === 'alerts' && <AlertsTab />}
      {tab === 'maintenance' && <MaintenanceTab />}
    </div>
  );
}

const STRATEGIES: { id: string; label: string; desc: string }[] = [
  { id: 'cheapest', label: 'Cheapest', desc: 'Pick the channel with the lowest total price per 1M tokens.' },
  { id: 'fastest', label: 'Fastest', desc: 'Pick the highest-priority channel (priority acts as a latency proxy).' },
  { id: 'balanced', label: 'Balanced', desc: 'Min-max normalize price and priority, then minimize the weighted sum.' },
];

function RoutingTab() {
  const [strategy, setStrategy] = useState('cheapest');
  const [savedStrategy, setSavedStrategy] = useState('cheapest');
  const [markup, setMarkup] = useState(1.0);
  const [savedMarkup, setSavedMarkup] = useState(1.0);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.getConfig().then((r) => {
      setStrategy(r.cost_strategy);
      setSavedStrategy(r.cost_strategy);
      setMarkup(r.markup_ratio);
      setSavedMarkup(r.markup_ratio);
    }).catch((e) => setError(e?.message || 'failed'));
  }, []);

  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      const r = await api.updateConfig({ cost_strategy: strategy, markup_ratio: markup });
      setSavedStrategy(r.cost_strategy);
      setSavedMarkup(r.markup_ratio);
    } catch (e: any) {
      setError(e?.message || 'failed');
    } finally {
      setSaving(false);
    }
  };

  const dirty = strategy !== savedStrategy || markup !== savedMarkup;

  return (
    <div>
      {error && (
        <div className="card p-3 mb-4 bg-red-50 border-red-200 text-red-700 text-sm">{error}</div>
      )}

      <div className="card p-5 mb-4">
        <h2 className="text-sm font-semibold text-slate-700 mb-1">L3 Cost Strategy</h2>
        <p className="text-xs text-slate-500 mb-4">
          How the router orders candidate channels after L1 static match and L2 circuit-breaker filter.
        </p>
        <div className="space-y-2">
          {STRATEGIES.map((s) => {
            const active = strategy === s.id;
            return (
              <label
                key={s.id}
                className={`flex items-start gap-3 p-3 rounded border cursor-pointer ${
                  active ? 'border-brand-500 bg-brand-50/40' : 'border-slate-200 hover:bg-slate-50'
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
      </div>

      <div className="card p-5 mb-4">
        <h2 className="text-sm font-semibold text-slate-700 mb-1">Billing Markup</h2>
        <p className="text-xs text-slate-500 mb-4">
          Per-request billed cost = real cost × markup. Default 1.0 (no markup). Changes take effect immediately.
        </p>
        <div className="flex items-center gap-3">
          <input
            type="number"
            step="0.05"
            min="0.01"
            max="1000"
            className="input w-32"
            value={markup}
            onChange={(e) => setMarkup(parseFloat(e.target.value) || 0)}
          />
          <span className="text-xs text-slate-500">× real cost</span>
        </div>
      </div>

      <div className="flex items-center justify-between">
        <div className="text-xs text-slate-500">
          {dirty && <span className="text-amber-600">Unsaved changes</span>}
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
  );
}

function SecurityTab() {
  const [old, setOld] = useState('');
  const [pw, setPw] = useState('');
  const [pw2, setPw2] = useState('');
  const [saving, setSaving] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setMsg(null);
    setError(null);
    if (pw.length < 6) {
      setError('Password must be at least 6 characters');
      return;
    }
    if (pw !== pw2) {
      setError('Passwords do not match');
      return;
    }
    setSaving(true);
    try {
      await api.changePassword(1, { old_password: old, new_password: pw });
      setMsg('Password updated. All sessions for this user have been invalidated.');
      setOld('');
      setPw('');
      setPw2('');
    } catch (e: any) {
      setError(e?.message || 'failed');
    } finally {
      setSaving(false);
    }
  };

  return (
    <form onSubmit={submit}>
      {error && (
        <div className="card p-3 mb-4 bg-red-50 border-red-200 text-red-700 text-sm">{error}</div>
      )}
      {msg && (
        <div className="card p-3 mb-4 bg-green-50 border-green-200 text-green-700 text-sm">{msg}</div>
      )}

      <div className="card p-5 mb-4">
        <h2 className="text-sm font-semibold text-slate-700 mb-1">Change admin password</h2>
        <p className="text-xs text-slate-500 mb-4">
          Admins can change their own password. After saving, all active sessions for this user
          are invalidated and must log in again with the new password.
        </p>

        <label className="block text-sm mb-3">
          <span className="text-slate-600">Current password</span>
          <input
            type="password"
            className="input mt-1"
            value={old}
            onChange={(e) => setOld(e.target.value)}
            autoComplete="current-password"
          />
        </label>
        <label className="block text-sm mb-3">
          <span className="text-slate-600">New password</span>
          <input
            type="password"
            className="input mt-1"
            value={pw}
            onChange={(e) => setPw(e.target.value)}
            autoComplete="new-password"
          />
        </label>
        <label className="block text-sm mb-3">
          <span className="text-slate-600">Confirm new password</span>
          <input
            type="password"
            className="input mt-1"
            value={pw2}
            onChange={(e) => setPw2(e.target.value)}
            autoComplete="new-password"
          />
        </label>

        <button type="submit" disabled={saving} className="btn-primary">
          {saving ? 'Saving…' : 'Update password'}
        </button>
      </div>
    </form>
  );
}

function AlertsTab() {
  const [alerts, setAlerts] = useState<any[]>([]);
  const [events, setEvents] = useState<any[]>([]);
  const [showNew, setShowNew] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const reload = () => {
    api.listAlerts().then((r) => setAlerts(r.data)).catch((e) => setError(e?.message || 'failed'));
    api.listAlertEvents(50).then((r) => setEvents(r.data)).catch(() => {});
  };
  useEffect(reload, []);

  const toggle = async (a: any) => {
    try {
      await api.updateAlert(a.id, { enabled: !a.enabled });
      reload();
    } catch (e: any) {
      setError(e?.message || 'failed');
    }
  };

  const remove = async (id: number) => {
    if (!confirm('Delete this alert?')) return;
    await api.deleteAlert(id);
    reload();
  };

  const ack = async (id: number) => {
    await api.ackAlertEvent(id);
    reload();
  };

  return (
    <div>
      {error && (
        <div className="card p-3 mb-4 bg-red-50 border-red-200 text-red-700 text-sm">{error}</div>
      )}

      <div className="card p-5 mb-4">
        <div className="flex items-center justify-between mb-3">
          <h2 className="text-sm font-semibold text-slate-700">Alert rules</h2>
          <button onClick={() => setShowNew(!showNew)} className="btn-primary text-xs">
            {showNew ? 'Cancel' : 'New rule'}
          </button>
        </div>

        {showNew && <NewAlertForm onCreated={() => { setShowNew(false); reload(); }} />}

        <table className="w-full text-sm">
          <thead className="text-left text-xs text-slate-500 border-b border-slate-100">
            <tr>
              <th className="py-2">Name</th>
              <th>Type</th>
              <th>Threshold</th>
              <th>Window</th>
              <th>Cooldown</th>
              <th>Enabled</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {alerts.length === 0 && (
              <tr>
                <td colSpan={7} className="text-center text-slate-400 py-6">
                  No alert rules configured.
                </td>
              </tr>
            )}
            {alerts.map((a) => (
              <tr key={a.id} className="border-t border-slate-100">
                <td className="py-2 font-medium">{a.name}</td>
                <td className="text-slate-600">{a.type}</td>
                <td className="text-slate-600">{a.threshold}</td>
                <td className="text-slate-600">{a.window_sec}s</td>
                <td className="text-slate-600">{a.cooldown_sec}s</td>
                <td>
                  <input type="checkbox" checked={a.enabled} onChange={() => toggle(a)} />
                </td>
                <td>
                  <button
                    onClick={() => remove(a.id)}
                    className="text-xs text-red-600 hover:underline"
                  >
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className="card p-5">
        <h2 className="text-sm font-semibold text-slate-700 mb-3">Recent events</h2>
        <table className="w-full text-sm">
          <thead className="text-left text-xs text-slate-500 border-b border-slate-100">
            <tr>
              <th className="py-2">When</th>
              <th>Name</th>
              <th>Type</th>
              <th>Webhook</th>
              <th>Ack</th>
            </tr>
          </thead>
          <tbody>
            {events.length === 0 && (
              <tr>
                <td colSpan={5} className="text-center text-slate-400 py-6">
                  No events fired.
                </td>
              </tr>
            )}
            {events.map((e) => (
              <tr key={e.id} className="border-t border-slate-100">
                <td className="py-2 text-slate-500 text-xs">
                  {e.fired_at.replace('T', ' ').slice(0, 19)}
                </td>
                <td>{e.alert_name}</td>
                <td className="text-slate-600">{e.alert_type}</td>
                <td>
                  <span
                    className={`badge ${
                      e.delivered_webhook
                        ? 'bg-green-50 text-green-700 border border-green-200'
                        : 'bg-slate-50 text-slate-500 border border-slate-200'
                    }`}
                  >
                    {e.delivered_webhook ? 'delivered' : '—'}
                  </span>
                </td>
                <td>
                  {e.acknowledged ? (
                    <span className="text-xs text-slate-400">acked</span>
                  ) : (
                    <button
                      onClick={() => ack(e.id)}
                      className="text-xs text-brand-700 hover:underline"
                    >
                      Ack
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function NewAlertForm({ onCreated }: { onCreated: () => void }) {
  const [name, setName] = useState('');
  const [type, setType] = useState<any>('error_rate');
  const [threshold, setThreshold] = useState(0.5);
  const [windowSec, setWindowSec] = useState(300);
  const [cooldownSec, setCooldownSec] = useState(300);
  const [webhookUrl, setWebhookUrl] = useState('');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setSaving(true);
    setError(null);
    try {
      await api.createAlert({
        name,
        type,
        threshold,
        window_sec: windowSec,
        cooldown_sec: cooldownSec,
        webhook_url: webhookUrl,
        enabled: true,
      });
      onCreated();
    } catch (e: any) {
      setError(e?.message || 'failed');
    } finally {
      setSaving(false);
    }
  };

  return (
    <form onSubmit={submit} className="border border-slate-200 rounded p-3 mb-4 bg-slate-50/40">
      {error && <div className="text-xs text-red-600 mb-2">{error}</div>}
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <label className="text-xs">
          <span className="text-slate-500">Name</span>
          <input className="input mt-1" value={name} onChange={(e) => setName(e.target.value)} required />
        </label>
        <label className="text-xs">
          <span className="text-slate-500">Type</span>
          <select className="input mt-1" value={type} onChange={(e) => setType(e.target.value as any)}>
            <option value="error_rate">error_rate (ratio 0..1)</option>
            <option value="p95_latency">p95_latency (ms)</option>
            <option value="cost_spike">cost_spike (multiplier)</option>
            <option value="key_exhausted">key_exhausted</option>
          </select>
        </label>
        <label className="text-xs">
          <span className="text-slate-500">Threshold</span>
          <input
            type="number"
            step="0.01"
            className="input mt-1"
            value={threshold}
            onChange={(e) => setThreshold(parseFloat(e.target.value) || 0)}
          />
        </label>
        <label className="text-xs">
          <span className="text-slate-500">Window (sec)</span>
          <input
            type="number"
            className="input mt-1"
            value={windowSec}
            onChange={(e) => setWindowSec(parseInt(e.target.value, 10) || 0)}
          />
        </label>
        <label className="text-xs">
          <span className="text-slate-500">Cooldown (sec)</span>
          <input
            type="number"
            className="input mt-1"
            value={cooldownSec}
            onChange={(e) => setCooldownSec(parseInt(e.target.value, 10) || 0)}
          />
        </label>
        <label className="text-xs sm:col-span-2">
          <span className="text-slate-500">Webhook URL (optional)</span>
          <input
            type="url"
            className="input mt-1"
            value={webhookUrl}
            onChange={(e) => setWebhookUrl(e.target.value)}
            placeholder="https://hooks.example.com/..."
          />
        </label>
      </div>
      <button type="submit" disabled={saving} className="btn-primary mt-3">
        {saving ? 'Creating…' : 'Create rule'}
      </button>
    </form>
  );
}

function MaintenanceTab() {
  const [breakerMax, setBreakerMax] = useState(5);
  const [breakerResetMs, setBreakerResetMs] = useState(30000);
  const [alertCooldown, setAlertCooldown] = useState(300);
  const [logRetention, setLogRetention] = useState(30);
  const [streamTimeout, setStreamTimeout] = useState(300);
  const [streamMaxBody, setStreamMaxBody] = useState(32 * 1024 * 1024);
  const [maxLogSubscribers, setMaxLogSubscribers] = useState(0);
  const [logLevel, setLogLevel] = useState(1);
  const [saved, setSaved] = useState<any>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.getConfig().then((r) => {
      setBreakerMax(r.breaker_max_failures);
      setBreakerResetMs(r.breaker_reset_timeout_ms);
      setAlertCooldown(r.alert_cooldown_sec);
      setLogRetention(r.log_retention_days);
      setStreamTimeout(r.stream_timeout_sec);
      setStreamMaxBody(r.stream_max_body_bytes);
      setMaxLogSubscribers(r.max_log_subscribers);
      setLogLevel(r.log_level);
      setSaved({
        breaker_max_failures: r.breaker_max_failures,
        breaker_reset_timeout_ms: r.breaker_reset_timeout_ms,
        alert_cooldown_sec: r.alert_cooldown_sec,
        log_retention_days: r.log_retention_days,
        stream_timeout_sec: r.stream_timeout_sec,
        stream_max_body_bytes: r.stream_max_body_bytes,
        max_log_subscribers: r.max_log_subscribers,
        log_level: r.log_level,
      });
    }).catch((e) => setError(e?.message || 'failed'));
  }, []);

  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      const r = await api.updateConfig({
        breaker_max_failures: breakerMax,
        breaker_reset_timeout_ms: breakerResetMs,
        alert_cooldown_sec: alertCooldown,
        log_retention_days: logRetention,
        stream_timeout_sec: streamTimeout,
        stream_max_body_bytes: streamMaxBody,
        max_log_subscribers: maxLogSubscribers,
        log_level: logLevel,
      });
      setSaved({
        breaker_max_failures: r.breaker_max_failures,
        breaker_reset_timeout_ms: r.breaker_reset_timeout_ms,
        alert_cooldown_sec: r.alert_cooldown_sec,
        log_retention_days: r.log_retention_days,
        stream_timeout_sec: r.stream_timeout_sec,
        stream_max_body_bytes: r.stream_max_body_bytes,
        max_log_subscribers: r.max_log_subscribers,
        log_level: r.log_level,
      });
    } catch (e: any) {
      setError(e?.message || 'failed');
    } finally {
      setSaving(false);
    }
  };

  const dirty =
    saved && (
      saved.breaker_max_failures !== breakerMax ||
      saved.breaker_reset_timeout_ms !== breakerResetMs ||
      saved.alert_cooldown_sec !== alertCooldown ||
      saved.log_retention_days !== logRetention ||
      saved.stream_timeout_sec !== streamTimeout ||
      saved.stream_max_body_bytes !== streamMaxBody ||
      saved.max_log_subscribers !== maxLogSubscribers ||
      saved.log_level !== logLevel
    );

  return (
    <div>
      {error && (
        <div className="card p-3 mb-4 bg-red-50 border-red-200 text-red-700 text-sm">{error}</div>
      )}

      <div className="card p-5 mb-4">
        <h2 className="text-sm font-semibold text-slate-700 mb-1">Circuit breaker defaults</h2>
        <p className="text-xs text-slate-500 mb-4">
          Applied to channels that don't override these values per-channel. Changes affect new
          requests only.
        </p>
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <NumberField
            label="Max failures"
            value={breakerMax}
            onChange={setBreakerMax}
            min={1}
            max={1000}
          />
          <NumberField
            label="Reset timeout (ms)"
            value={breakerResetMs}
            onChange={setBreakerResetMs}
            min={100}
            max={86_400_000}
          />
        </div>
      </div>

      <div className="card p-5 mb-4">
        <h2 className="text-sm font-semibold text-slate-700 mb-1">Streaming caps</h2>
        <p className="text-xs text-slate-500 mb-4">
          Per-stream limits applied to /v1/chat/completions when stream=true. Set to 0 to disable.
        </p>
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <NumberField
            label="Timeout (sec, 0 = disabled)"
            value={streamTimeout}
            onChange={setStreamTimeout}
            min={0}
            max={3600}
          />
          <NumberField
            label="Max body bytes (0 = unlimited)"
            value={streamMaxBody}
            onChange={setStreamMaxBody}
            min={0}
            max={1 << 30}
          />
        </div>
      </div>

      <div className="card p-5 mb-4">
        <h2 className="text-sm font-semibold text-slate-700 mb-1">Alerts</h2>
        <NumberField
          label="Default cooldown (sec)"
          value={alertCooldown}
          onChange={setAlertCooldown}
          min={0}
          max={86_400}
        />
      </div>

      <div className="card p-5 mb-4">
        <h2 className="text-sm font-semibold text-slate-700 mb-1">Logs retention</h2>
        <p className="text-xs text-slate-500 mb-4">
          Logs older than this many days are deleted by a daily background sweep. Set to 0 to keep
          logs indefinitely.
        </p>
        <NumberField
          label="Retention (days)"
          value={logRetention}
          onChange={setLogRetention}
          min={0}
          max={3650}
        />
      </div>

      <div className="card p-5 mb-4">
        <h2 className="text-sm font-semibold text-slate-700 mb-1">Log broker & level</h2>
        <p className="text-xs text-slate-500 mb-4">
          Cap concurrent SSE log subscribers and choose the minimum log level emitted to stderr.
        </p>
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <NumberField
            label="Max SSE subscribers (0 = unlimited)"
            value={maxLogSubscribers}
            onChange={setMaxLogSubscribers}
            min={0}
            max={100000}
          />
          <label className="block text-sm">
            <span className="text-slate-600">Log level</span>
            <select
              className="input mt-1"
              value={logLevel}
              onChange={(e) => setLogLevel(parseInt(e.target.value, 10))}
            >
              <option value={0}>0 — debug (everything)</option>
              <option value={1}>1 — info (default)</option>
              <option value={2}>2 — warn (drop info)</option>
              <option value={3}>3 — error (drop everything below)</option>
            </select>
          </label>
        </div>
      </div>

      <div className="flex items-center justify-between">
        <div className="text-xs text-slate-500">
          {dirty && <span className="text-amber-600">Unsaved changes</span>}
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
  );
}

function NumberField({
  label,
  value,
  onChange,
  min,
  max,
}: {
  label: string;
  value: number;
  onChange: (v: number) => void;
  min?: number;
  max?: number;
}) {
  return (
    <label className="block text-sm">
      <span className="text-slate-600">{label}</span>
      <input
        type="number"
        className="input mt-1"
        value={value}
        min={min}
        max={max}
        onChange={(e) => onChange(parseInt(e.target.value, 10) || 0)}
      />
    </label>
  );
}
