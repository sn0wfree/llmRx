import { ReactNode } from 'react';

interface Props {
  ok: boolean;
  children: ReactNode;
}

export default function StatusBadge({ ok, children }: Props) {
  return (
    <span
      className={`badge ${
        ok ? 'bg-green-50 text-green-700 border border-green-200' : 'bg-red-50 text-red-700 border border-red-200'
      }`}
    >
      {children}
    </span>
  );
}