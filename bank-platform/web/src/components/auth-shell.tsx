import Link from "next/link";
import { ReactNode } from "react";
import { Logo } from "./logo";

export function AuthShell({ title, subtitle, children, footer }: { title: string; subtitle?: ReactNode; children: ReactNode; footer?: ReactNode }) {
  return (
    <main className="grid min-h-screen lg:grid-cols-[1fr_1.1fr]">
      <div className="hidden flex-col justify-between bg-gradient-to-br from-brand-900 to-brand-700 p-10 text-white lg:flex">
        <Link href="/">
          <Logo light />
        </Link>
        <div>
          <p className="text-3xl font-semibold leading-snug">Every ngultrum accounted for.</p>
          <p className="mt-3 max-w-md text-white/70">
            Double-entry ledger, saga-based transfers, real-time fraud screening and a hash-chained audit trail behind a
            single API gateway.
          </p>
        </div>
        <p className="text-xs text-white/50">DSO202 final project · demonstration system</p>
      </div>
      <div className="flex items-center justify-center px-4 py-10">
        <div className="w-full max-w-md">
          <div className="mb-8 lg:hidden">
            <Link href="/">
              <Logo />
            </Link>
          </div>
          <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
          {subtitle && <p className="mt-1.5 text-sm text-muted">{subtitle}</p>}
          <div className="mt-6">{children}</div>
          {footer && <div className="mt-6 text-sm text-muted">{footer}</div>}
        </div>
      </div>
    </main>
  );
}
