"use client";

import Link from "next/link";
import { useState } from "react";
import { useRouter } from "next/navigation";
import { AuthShell } from "@/components/auth-shell";
import { Alert, Button, Field } from "@/components/ui";
import { api, errMsg } from "@/lib/api";

export default function RegisterPage() {
  const router = useRouter();
  const [f, setF] = useState({ full_name: "", email: "", phone: "", password: "", confirm: "" });
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const set = (k: keyof typeof f) => (e: React.ChangeEvent<HTMLInputElement>) => setF({ ...f, [k]: e.target.value });

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    if (f.password !== f.confirm) return setError("Passwords do not match");
    setLoading(true);
    try {
      const r = await api<{ message: string }>("/v1/auth/register", {
        body: { full_name: f.full_name, email: f.email, phone: f.phone, password: f.password },
      });
      const devCode = r.message.match(/code: (\d{6})/)?.[1];
      router.push(`/verify?email=${encodeURIComponent(f.email)}${devCode ? `&dev=${devCode}` : ""}`);
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setLoading(false);
    }
  }

  return (
    <AuthShell
      title="Open your account"
      subtitle="It takes about two minutes. You'll verify your email, then your identity."
      footer={<>Already a customer? <Link href="/login" className="font-medium text-brand-600 hover:underline">Sign in</Link></>}
    >
      <form onSubmit={submit} className="space-y-4">
        {error && <Alert>{error}</Alert>}
        <Field label="Full name (as on your CID)">
          <input className="input" required autoComplete="name" value={f.full_name} onChange={set("full_name")} />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Email">
            <input className="input" type="email" required autoComplete="email" value={f.email} onChange={set("email")} />
          </Field>
          <Field label="Mobile number">
            <input className="input" inputMode="tel" placeholder="17xxxxxx" autoComplete="tel" value={f.phone} onChange={set("phone")} />
          </Field>
        </div>
        <Field label="Password" hint="At least 8 characters with letters and numbers.">
          <input className="input" type="password" required minLength={8} autoComplete="new-password" value={f.password} onChange={set("password")} />
        </Field>
        <Field label="Confirm password">
          <input className="input" type="password" required autoComplete="new-password" value={f.confirm} onChange={set("confirm")} />
        </Field>
        <Button className="w-full" loading={loading}>Create account</Button>
      </form>
    </AuthShell>
  );
}
