"use client";

import Link from "next/link";
import { Suspense, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { AuthShell } from "@/components/auth-shell";
import { Alert, Button, Field } from "@/components/ui";
import { errMsg, session } from "@/lib/api";

const demo = [
  ["admin@bank.local", "Admin"],
  ["ops@bank.local", "Operations"],
  ["loans@bank.local", "Loans"],
  ["fraud@bank.local", "Fraud"],
  ["auditor@bank.local", "Auditor"],
];

function LoginForm() {
  const router = useRouter();
  const params = useSearchParams();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [mfaToken, setMfaToken] = useState<string | null>(null);
  const [code, setCode] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  const done = (user: any) => {
    const staff = user?.roles?.some((r: string) => r !== "customer");
    const next = params.get("next");
    router.replace(next && next !== "/" ? next : staff ? "/staff" : "/dashboard");
    router.refresh();
  };

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    setLoading(true);
    try {
      if (mfaToken) {
        const r = await session("mfa", { mfa_token: mfaToken, code });
        done(r.user);
      } else {
        const r = await session<{ mfa_required: boolean; mfa_token?: string; user: any }>("login", { email, password });
        if (r.mfa_required && r.mfa_token) setMfaToken(r.mfa_token);
        else done(r.user);
      }
    } catch (e: any) {
      if (e.status === 412 || /not verified/i.test(e.message)) {
        router.push(`/verify?email=${encodeURIComponent(email)}`);
        return;
      }
      setError(errMsg(e));
    } finally {
      setLoading(false);
    }
  }

  return (
    <AuthShell
      title={mfaToken ? "Two-step verification" : "Sign in"}
      subtitle={mfaToken ? "Enter the 6-digit code we just emailed you." : "Welcome back to CBMP Bank."}
      footer={
        !mfaToken && (
          <>
            New here?{" "}
            <Link href="/register" className="font-medium text-brand-600 hover:underline">
              Open an account
            </Link>
          </>
        )
      }
    >
      <form onSubmit={submit} className="space-y-4">
        {error && <Alert>{error}</Alert>}
        {mfaToken ? (
          <>
            <Field label="Verification code" hint={<>Development: codes arrive in <a className="underline" href="http://localhost:8025" target="_blank">Mailpit</a>.</>}>
              <input className="input text-center text-lg tracking-[0.5em]" inputMode="numeric" maxLength={6} autoFocus value={code} onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))} />
            </Field>
            <Button className="w-full" loading={loading} disabled={code.length !== 6}>Verify and sign in</Button>
            <button type="button" className="w-full text-sm text-muted hover:underline" onClick={() => { setMfaToken(null); setCode(""); }}>
              Start over
            </button>
          </>
        ) : (
          <>
            <Field label="Email">
              <input className="input" type="email" autoComplete="email" required value={email} onChange={(e) => setEmail(e.target.value)} />
            </Field>
            <Field label="Password">
              <input className="input" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
            </Field>
            <Button className="w-full" loading={loading}>Sign in</Button>
          </>
        )}
      </form>
      {!mfaToken && (
        <div className="mt-8 rounded-2xl border border-dashed border-base p-4">
          <p className="text-xs font-semibold uppercase tracking-wide text-muted">Demo staff accounts · password Bank@12345</p>
          <div className="mt-2 flex flex-wrap gap-1.5">
            {demo.map(([e, l]) => (
              <button key={e} type="button" onClick={() => { setEmail(e); setPassword("Bank@12345"); }}
                className="rounded-lg bg-[var(--surface-2)] px-2.5 py-1 text-xs font-medium hover:bg-brand-50 hover:text-brand-700">
                {l}
              </button>
            ))}
          </div>
        </div>
      )}
    </AuthShell>
  );
}

export default function LoginPage() {
  return (
    <Suspense>
      <LoginForm />
    </Suspense>
  );
}
