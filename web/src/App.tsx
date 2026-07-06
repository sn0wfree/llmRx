import { useEffect, useState } from 'react';
import { getSessionToken, setSessionToken } from './api';
import Login from './pages/Login';
import Layout from './components/Layout';
import Dashboard from './pages/Dashboard';
import Channels from './pages/Channels';
import Tokens from './pages/Tokens';
import Logs from './pages/Logs';
import Analytics from './pages/Analytics';

function readHash(): string {
  const h = window.location.hash.replace(/^#\/?/, '');
  return h || 'dashboard';
}

function navigate(path: string) {
  window.location.hash = path;
}

export default function App() {
  const [page, setPage] = useState<string>(readHash());
  const [authed, setAuthed] = useState<boolean>(!!getSessionToken());

  useEffect(() => {
    const onHash = () => setPage(readHash());
    window.addEventListener('hashchange', onHash);
    return () => window.removeEventListener('hashchange', onHash);
  }, []);

  const onLogin = () => {
    setAuthed(true);
    navigate('dashboard');
  };

  const onLogout = () => {
    setSessionToken(null);
    setAuthed(false);
    navigate('login');
  };

  if (!authed) {
    return <Login onSuccess={onLogin} />;
  }

  return (
    <Layout current={page} onNavigate={navigate} onLogout={onLogout}>
      {page === 'dashboard' && <Dashboard onNavigate={navigate} />}
      {page === 'channels' && <Channels />}
      {page === 'tokens' && <Tokens />}
      {page === 'logs' && <Logs />}
      {page === 'analytics' && <Analytics />}
      {page === 'settings' && <Settings />}
    </Layout>
  );
}

function Settings() {
  return (
    <div className="card p-6">
      <h2 className="text-lg font-semibold mb-2">Settings</h2>
      <p className="text-sm text-slate-500">
        Coming in P6. Configure rate limits, alerts, and Docker packaging.
      </p>
    </div>
  );
}