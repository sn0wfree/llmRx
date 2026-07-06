import { useEffect, useState } from 'react';
import { api } from '../api';

interface DashboardData {
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
}

function Card({ title, value, sub }: { title: string; value: string | number; sub?: string }) {
  return (
    <div className="card p-4">
      <div className="text-xs uppercase tracking-wide text-slate-500">{title}</div>
      <div className="text-2xl font-semibold mt-1">{value}</div>
      {sub && <div className="text-xs text-slate-400 mt-1">{sub}</div>}
    </div>
  );
}

export default function Dashboard({ onNavigate }: { onNavigate: (p: string) => void }) {
  const [data, setData] = useState<DashboardData | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    const tick = async () => {
      try {
        const d = await api.dashboard();
        if (alive) setData(d);
      } catch (e: any) {
        if (alive) setError(e?.message || 'failed');
      }
    };
    tick();
    const id = setInterval(tick, 10000);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, []);

  if (error) {
    return <div className="card p-6 text-red-700 bg-red-50 border-red-200">Error: {error}</div>;
  }
  if (!data) {
    return <div className="text-slate-500">Loading…</div>;
  }

  const errorRate = data.logs_total > 0 ? (data.logs_errors / data.logs_total) * 100 : 0;
  const totalKeys = Object.values(data.keys_by_channel).reduce((a, b) => a + b, 0);

  return (
    <div>
      <div className="flex items-end justify-between mb-6">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Dashboard</h1>
          <p className="text-sm text-slate-500 mt-1">Live snapshot, refreshes every 10s.</p>
        </div>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4 mb-6">
        <Card
          title="Channels"
          value={`${data.channels_active} / ${data.channels_total}`}
          sub="Active / total"
        />
        <Card
          title="API Keys"
          value={totalKeys}
          sub={`Across ${data.channels_active} channels`}
        />
        <Card
          title="Tokens"
          value={`${data.tokens_active} / ${data.tokens_total}`}
          sub="Active / total"
        />
        <Card
          title="Error rate"
          value={`${errorRate.toFixed(1)}%`}
          sub={`${data.logs_errors} of ${data.logs_total} requests`}
        />
        <Card
          title="Prompt tokens"
          value={data.prompt_tokens.toLocaleString()}
        />
        <Card
          title="Completion tokens"
          value={data.completion_tokens.toLocaleString()}
        />
        <Card
          title="Real cost (USD)"
          value={`$${data.real_cost_usd.toFixed(4)}`}
        />
        <Card
          title="Billed cost (USD)"
          value={`$${data.billed_cost_usd.toFixed(4)}`}
          sub="Includes markup"
        />
      </div>

      <div className="card p-4">
        <div className="flex items-center justify-between mb-3">
          <h2 className="font-semibold">Quick actions</h2>
        </div>
        <div className="flex flex-wrap gap-2">
          <button className="btn-ghost" onClick={() => onNavigate('channels')}>
            Manage channels
          </button>
          <button className="btn-ghost" onClick={() => onNavigate('tokens')}>
            Generate token
          </button>
          <button className="btn-ghost" onClick={() => onNavigate('logs')}>
            View logs
          </button>
        </div>
      </div>
    </div>
  );
}