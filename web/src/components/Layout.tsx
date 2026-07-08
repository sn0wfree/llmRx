import { ReactNode } from 'react';

interface Props {
  current: string;
  onNavigate: (p: string) => void;
  onLogout: () => void;
  children: ReactNode;
}

const NAV: { id: string; label: string; icon: string }[] = [
  { id: 'dashboard', label: 'Dashboard', icon: '📊' },
  { id: 'channels', label: 'Channels', icon: '📡' },
  { id: 'tokens', label: 'Tokens', icon: '🔑' },
  { id: 'plans', label: 'Plans', icon: '💳' },
  { id: 'logs', label: 'Logs', icon: '📋' },
  { id: 'analytics', label: 'Analytics', icon: '📈' },
  { id: 'settings', label: 'Settings', icon: '⚙️' },
];

export default function Layout({ current, onNavigate, onLogout, children }: Props) {
  return (
    <div className="flex min-h-screen">
      <aside className="w-56 bg-slate-900 text-slate-100 flex flex-col">
        <div className="px-5 py-4 border-b border-slate-800">
          <div className="text-lg font-semibold tracking-tight">llmRx</div>
          <div className="text-xs text-slate-400">LLM API gateway</div>
        </div>
        <nav className="flex-1 py-3">
          {NAV.map((item) => {
            const active = current === item.id;
            return (
              <button
                key={item.id}
                onClick={() => onNavigate(item.id)}
                className={`w-full flex items-center gap-3 px-5 py-2 text-sm transition ${
                  active
                    ? 'bg-brand-700 text-white border-l-2 border-brand-500'
                    : 'text-slate-300 hover:bg-slate-800 border-l-2 border-transparent'
                }`}
              >
                <span>{item.icon}</span>
                <span>{item.label}</span>
              </button>
            );
          })}
        </nav>
        <div className="border-t border-slate-800 p-3">
          <button onClick={onLogout} className="w-full btn-ghost text-slate-200 bg-slate-800 border-slate-700 hover:bg-slate-700">
            Logout
          </button>
        </div>
      </aside>

      <main className="flex-1 p-6 overflow-x-auto">{children}</main>
    </div>
  );
}