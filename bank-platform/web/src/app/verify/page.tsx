"use client";

import { Suspense, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { AuthShell } from "@/components/auth-shell";
import { Alert, Button, Field } from "@/components/ui";
import { api, errMsg } from "@/lib/api";

function Verify() {
  const router = useRouter();
  const params = useSearchParams();
  const [email, setEmail] = useState(params.get("email") ?? "");
  const [code, setCode] = useState("");
  const [devCode, setDevCode] = useState(params.get("dev") ?? "");
  const [error, setError] = useState("");
  const [info, setInfo] = useState("");
  const [loading, setLoading] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    setLoading(true);
    try {
      await api("/v1/auth/verify-email", { body: { email, code } });
      router.push("/login?verified=1");
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setLoading(false);
    }
  }

  async function resend() {
    setError("");
    try {
      const r = await api<{ message: string }>("/v1/auth/resend-code", { body: { email } });
      setInfo("A new code is on its way.");
      setDevCode(r.message.match(/code: (\d{6})/)?.[1] ?? "");
    } catch (e) {
      setError(errMsg(e));
    }
  }

  return (
    <AuthShell title="Verify your email" subtitle={<>We sent a 6-digit code to <b>{email || "your email"}</b>.</>}>
      <form onSubmit={submit} className="space-y-4">
        {error && <Alert>{error}</Alert>}
        {info && <Alert tone="green">{info}</Alert>}
        {devCode && (
          <Alert tone="blue">
            Development mode: your code is <b className="font-mono">{devCode}</b>. Real deployments only send it by email (see{" "}
            <a className="underline" href="http://localhost:8025" target="_blank">Mailpit</a>).
          </Alert>
        )}
        {!params.get("email") && (
          <Field label="Email">
            <input className="input" type="email" required value={email} onChange={(e) => setEmail(e.target.value)} />
          </Field>
        )}
        <Field label="Verification code">
          <input className="input text-center text-lg tracking-[0.5em]" inputMode="numeric" maxLength={6} autoFocus required
            value={code} onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))} />
        </Field>
        <Button className="w-full" loading={loading} disabled={code.length !== 6}>Verify email</Button>
        <button type="button" onClick={resend} className="w-full text-sm text-brand-600 hover:underline">Resend code</button>
      </form>
    </AuthShell>
  );
}

export default function VerifyPage() {
  return (
    <Suspense>
      <Verify />
    </Suspense>
  );
}
