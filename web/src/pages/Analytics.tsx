import { useEffect, useMemo, useState } from 'react';
import {
  ResponsiveContainer,
  LineChart,
  Line,
  CartesianGrid,
  XAxis,
  YAxis,
  Tooltip,
  Legend,
  BarChart,
  Bar,
} from 'recharts';
import { api, SeriesPoint, NamedMetric } from '../api';

type Range = '1h' | '24h' | '7d' | '30d';

const RANGE_OPTS: { id: Range; label: string; bucket: number; seconds: number }[] = [
  { id: '1h', label: 'Last 1h', bucket: 60, seconds: 3600 },
  { id: '24h', label: 'Last 24h', bucket: 3600, seconds: 86400 },
  { id: '7d', label: 'Last 7d', bucket: 86400, seconds: 604800 },
  { id: '30d', label: 'Last 30d', bucket: 86400, seconds: 2592000 },
];

function fmtBucket(secs: number, bucket: number): string {
  const d = new Date(secs * 1000);
  if (bucket >= 86400) return d.toISOString().slice(0, 10);
  if (bucket >= 3600) return d.toISOString().slice(5, 16).replace('T', ' ');
  return d.toISOString().slice(11, 16);
}

export default function Analytics() {
  const [range, setRange] = useState<Range>('24h');
  const [series, setSeries] = useState<SeriesPoint[]>([]);
  const [byModel, setByModel] = useState<NamedMetric[]>([]);
  const [byChannel, setByChannel] = useState<NamedMetric[]>([]);
  const [byToken, setByToken] = useState<NamedMetric[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const opt = RANGE_OPTS.find((r) => r.id === range)!;

  const reload = async () => {
    setError(null);
    setLoading(true);
    const to = new Date();
    const from = new Date(to.getTime() - opt.seconds * 1000);
    const f = { from: from.toISOString(), to: to.toISOString() };
    try {
      const [ts, m, ch, tk] = await Promise.all([
        api.analyticsTimeSeries(opt.bucket, f),
        api.analyticsByModel(10, f),
        api.analyticsByChannel(10, f),
        api.analyticsByToken(10, f),
      ]);
      setSeries(ts.data);
      setByModel(m.data);
      setByChannel(ch.data);
      setByToken(tk.data);
    } catch (e: any) {
      setError(e?.message || 'failed to load analytics');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    reload();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [range]);

  const totalReqs = useMemo(() => series.reduce((s, p) => s + p.requests, 0), [series]);
  const totalErrs = useMemo(() => series.reduce((s, p) => s + p.errors, 0), [series]);
  const totalCost = useMemo(
    () => series.reduce((s, p) => s + p.billed_cost_usd, 0),
    [series]
  );
  const totalTokens = useMemo(
    () => series.reduce((s, p) => s + p.prompt_tokens + p.completion_tokens, 0),
    [series]
  );

  const chartData = useMemo(
    () =>
      series.map((p) => ({
        ...p,
        label: fmtBucket(p.bucket, opt.bucket),
      })),
    [series, opt.bucket]
  );

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Analytics</h1>
          <p className="text-sm text-slate-500 mt-1">
            Time series, top consumers, and request mix.
          </p>
        </div>
        <div className="flex gap-2">
          {RANGE_OPTS.map((r) => (
            <button
              key={r.id}
              onClick={() => setRange(r.id)}
              className={`px-3 py-1.5 text-sm rounded border ${
                range === r.id
                  ? 'bg-brand-600 text-white border-brand-600'
                  : 'bg-white text-slate-700 border-slate-200 hover:bg-slate-50'
              }`}
            >
              {r.label}
            </button>
          ))}
        </div>
      </div>

      {error && (
        <div className="card p-3 mb-4 bg-red-50 border-red-200 text-red-700 text-sm">{error}</div>
      )}

      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4 mb-6">
        <Stat label="Requests" value={totalReqs.toLocaleString()} />
        <Stat label="Errors" value={totalErrs.toLocaleString()} accent={totalErrs > 0 ? 'red' : undefined} />
        <Stat label="Tokens" value={totalTokens.toLocaleString()} />
        <Stat label="Billed Cost" value={`$${totalCost.toFixed(4)}`} />
      </div>

      <div className="card p-4 mb-6">
        <div className="flex items-center justify-between mb-3">
          <h2 className="text-sm font-semibold text-slate-700">Requests over time</h2>
          {loading && <span className="text-xs text-slate-400">loading…</span>}
        </div>
        <div style={{ width: '100%', height: 280 }}>
          <ResponsiveContainer>
            <LineChart data={chartData}>
              <CartesianGrid strokeDasharray="3 3" stroke="#e2e8f0" />
              <XAxis dataKey="label" tick={{ fontSize: 11 }} stroke="#94a3b8" />
              <YAxis yAxisId="left" tick={{ fontSize: 11 }} stroke="#94a3b8" />
              <YAxis yAxisId="right" orientation="right" tick={{ fontSize: 11 }} stroke="#94a3b8" />
              <Tooltip />
              <Legend />
              <Line yAxisId="left" type="monotone" dataKey="requests" stroke="#0ea5e9" strokeWidth={2} dot={false} name="Requests" />
              <Line yAxisId="left" type="monotone" dataKey="errors" stroke="#ef4444" strokeWidth={2} dot={false} name="Errors" />
              <Line yAxisId="right" type="monotone" dataKey="billed_cost_usd" stroke="#10b981" strokeWidth={2} dot={false} name="Billed $" />
            </LineChart>
          </ResponsiveContainer>
        </div>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
        <TopCard title="By Model" rows={byModel} fmtValue={(r) => r.count.toLocaleString()} sub={(r) => `$${r.cost.toFixed(4)}`} />
        <TopCard title="By Channel" rows={byChannel} fmtValue={(r) => r.count.toLocaleString()} sub={(r) => `$${r.cost.toFixed(4)}`} />
        <TopCard title="By Token" rows={byToken} fmtValue={(r) => r.count.toLocaleString()} sub={(r) => `$${r.cost.toFixed(4)}`} />
      </div>

      <div className="card p-4 mt-6">
        <h2 className="text-sm font-semibold text-slate-700 mb-3">Token × Model mix</h2>
        <div style={{ width: '100%', height: 240 }}>
          <ResponsiveContainer>
            <BarChart data={byToken}>
              <CartesianGrid strokeDasharray="3 3" stroke="#e2e8f0" />
              <XAxis dataKey="label" tick={{ fontSize: 11 }} stroke="#94a3b8" />
              <YAxis tick={{ fontSize: 11 }} stroke="#94a3b8" />
              <Tooltip />
              <Bar dataKey="count" fill="#0ea5e9" name="Requests" />
            </BarChart>
          </ResponsiveContainer>
        </div>
      </div>
    </div>
  );
}

function Stat({ label, value, accent }: { label: string; value: string; accent?: 'red' }) {
  return (
    <div className="card p-4">
      <div className="text-xs text-slate-500 uppercase tracking-wide">{label}</div>
      <div className={`text-2xl font-semibold mt-1 ${accent === 'red' ? 'text-red-600' : 'text-slate-800'}`}>
        {value}
      </div>
    </div>
  );
}

function TopCard({
  title,
  rows,
  fmtValue,
  sub,
}: {
  title: string;
  rows: NamedMetric[];
  fmtValue: (r: NamedMetric) => string;
  sub: (r: NamedMetric) => string;
}) {
  return (
    <div className="card p-4">
      <h2 className="text-sm font-semibold text-slate-700 mb-3">{title}</h2>
      {rows.length === 0 ? (
        <div className="text-sm text-slate-400 py-6 text-center">No data</div>
      ) : (
        <ul className="space-y-2">
          {rows.map((r) => (
            <li key={r.label} className="flex items-center justify-between text-sm">
              <span className="text-slate-700 truncate" title={r.label}>{r.label}</span>
              <span className="flex items-center gap-3">
                <span className="text-slate-500 text-xs">{sub(r)}</span>
                <span className="font-medium tabular-nums">{fmtValue(r)}</span>
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
