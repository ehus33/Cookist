import type { ReactNode } from 'react';

export function Header({ children }: { children: ReactNode }) {
  return <header className="header wrap"><a className="brand" href="/">cookist<span>.</span></a>{children}</header>;
}
