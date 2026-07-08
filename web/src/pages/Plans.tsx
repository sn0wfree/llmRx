import { FormEvent, useEffect, useState } from 'react';
import { api, Plan } from '../api';

export default function Plans() {
  const [items, setItems] = useState<Plan[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Plan | null>(null);

  const reload = () => {
    api.listPlans().then((r) => setItems(r.data)).catch((e) => setError(e?.message || 'failed'));
  };
  useEffect(reload, []);

  const remove = async (p: Plan) => {
    if (!confirm(`Delete plan "${p.name}"? Tokens bound to it will be unlinked (plan_id set to 0).`)) return;
    try {
      await api.deletePlan(p.id);
      reload();
    } catch (e: any) {
      setError(e?.message || 'delete failed');
    }
  };

  return (
    <div>
      <div className="flex items-end justify-between mb-6">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Plans</h1>
          <p className="text-sm text-slate-500 mt-1">
            Billing plans. A plan groups a per-token spend budget with a markup multiplier applied on top of
            per-channel pricing. Tokens reference a plan via <code className="text-xs">plan_id</code>.
          </p>
        </div>
        <button className="btn-primary" onClick={() => { setCreating(true); setEditing(null); }}>
          {creating ? 'Cancel' : '+ New plan'}
        </button>
      </div>

      {error && (
        <div className="card p-3 mb-4 bg-red-50 border-red-200 text-red-700 text-sm">{error}</div>
      )}

      {creating && (
        <PlanForm
          onSaved={() => { setCreating(false); reload(); }}
          onCancel={() => setCreating(false)}
        />
      )}
      {editing && (
        <PlanForm
          plan={editing}
          onSaved={() => { setEditing(null); reload(); }}
          onCancel={() => setEditing(null)}
        />
      )}

      <div className="card overflow-hidden">
        <table className="w-full text-sm">
          <thead className="bg-slate-50 text-left">
            <tr>
              <th className="px-4 py-2 font-medium text-slate-600">ID</th>
              <th className="px-4 py-2 font-medium text-slate-600">Name</th>
              <th className="px-4 py-2 font-medium text-slate-600">Budget (USD)</th>
              <th className="px-4 py-2 font-medium text-slate-600">Used (USD)</th>
              <th className="px-4 py-2 font-medium text-slate-600">Markup ×</th>
              <th className="px-4 py-2 font-medium text-slate-600">Status</th>
              <th className="px-4 py-2 font-medium text-slate-600">Actions</th>
            </tr>
          </thead>
          <tbody>
            {items.map((p) => (
              <tr key={p.id} className="border-t border-slate-100">
                <td className="px-4 py-2 text-slate-500">{p.id}</td>
                <td className="px-4 py-2 font-medium">{p.name}</td>
                <td className="px-4 py-2 text-slate-600">${p.budget_usd.toFixed(2)}</td>
                <td className="px-4 py-2 text-slate-600">${p.used_usd.toFixed(4)}</td>
                <td className="px-4 py-2 text-slate-600">{p.markup_ratio.toFixed(2)}</td>
                <td className="px-4 py-2">
                  <span
                    className={`badge ${
                      p.status === 1
                        ? 'bg-green-50 text-green-700 border border-green-200'
                        : 'bg-slate-100 text-slate-600 border border-slate-200'
                    }`}
                  >
                    {p.status === 1 ? 'active' : 'disabled'}
                  </span>
                </td>
                <td className="px-4 py-2">
                  <button className="btn-ghost mr-1" onClick={() => { setEditing(p); setCreating(false); }}>
                    Edit
                  </button>
                  <button className="btn-danger" onClick={() => remove(p)}>
                    Delete
                  </button>
                </td>
              </tr>
            ))}
            {items.length === 0 && (
              <tr>
                <td className="px-4 py-6 text-center text-slate-400" colSpan={7}>
                  No plans yet.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

interface PlanFormProps {
  plan?: Plan;
  onSaved: () => void;
  onCancel: () => void;
}

function PlanForm({ plan, onSaved, onCancel }: PlanFormProps) {
  const isEdit = !!plan;
  const [name, setName] = useState(plan?.name ?? '');
  const [budget, setBudget] = useState(plan?.budget_usd ?? 0);
  const [markup, setMarkup] = useState(plan?.markup_ratio ?? 1.0);
  const [status, setStatus] = useState(plan?.status ?? 1);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setSaving(true);
    setError(null);
    try {
      if (isEdit) {
        await api.updatePlan(plan!.id, {
          name,
          budget_usd: budget,
          markup_ratio: markup,
          status,
        });
      } else {
        await api.createPlan({
          name,
          budget_usd: budget,
          markup_ratio: markup,
          status,
        });
      }
      onSaved();
    } catch (e: any) {
      setError(e?.message || 'save failed');
    } finally {
      setSaving(false);
    }
  };

  return (
    <form onSubmit={submit} className="card p-6 mb-6">
      {error && <div className="text-xs text-red-600 mb-3">{error}</div>}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div>
          <label className="label">Name</label>
          <input className="input" required value={name} onChange={(e) => setName(e.target.value)} />
        </div>
        <div>
          <label className="label">Status</label>
          <select className="input" value={status} onChange={(e) => setStatus(Number(e.target.value))}>
            <option value={1}>active</option>
            <option value={2}>disabled</option>
          </select>
        </div>
        <div>
          <label className="label">Budget (USD, 0 = unlimited)</label>
          <input
            className="input"
            type="number"
            step="0.01"
            min="0"
            value={budget}
            onChange={(e) => setBudget(parseFloat(e.target.value) || 0)}
          />
        </div>
        <div>
          <label className="label">Markup ratio (× real cost)</label>
          <input
            className="input"
            type="number"
            step="0.05"
            min="0.01"
            value={markup}
            onChange={(e) => setMarkup(parseFloat(e.target.value) || 1.0)}
          />
        </div>
      </div>
      <div className="mt-6 flex gap-2">
        <button className="btn-primary" type="submit" disabled={saving}>
          {saving ? 'Saving…' : isEdit ? 'Update' : 'Create'}
        </button>
        <button className="btn-ghost" type="button" onClick={onCancel}>
          Cancel
        </button>
      </div>
    </form>
  );
}
