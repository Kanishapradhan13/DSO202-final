"use client";

import { useState } from "react";
import { ShieldCheck } from "lucide-react";
import { Alert, Badge, Button, Card, Field, PageHeader, useToast } from "@/components/ui";
import { useMe } from "@/components/me";
import { api, errMsg, useApi } from "@/lib/api";
import { date } from "@/lib/format";

export default function SettingsPage() {
  const { me, reload } = useMe();
  const toast = useToast();
  const profile = useApi("/v1/customers/me");
  const [pw, setPw] = useState({ current_password: "", new_password: "", confirm: "" });
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [edit, setEdit] = useState<{ phone?: string; address?: string; city?: string; occupation?: string }>({});

  async function toggleMfa() {
    try { await api("/v1/auth/mfa", { body: { enabled: !me?.mfa_enabled } }); toast(me?.mfa_enabled ? "Two-step verification off" : "Two-step verification on"); reload(); }
    catch (e) { toast(errMsg(e), "red"); }
  }

  return (
    <>
      <PageHeader title="Settings" />
      <div className="grid gap-6 lg:grid-cols-2">
        <Card title="Your sign-in">
          <dl className="space-y-3 text-sm">
            <div className="flex justify-between"><dt className="text-muted">Name</dt><dd className="font-medium">{me?.full_name}</dd></div>
            <div className="flex justify-between"><dt className="text-muted">Email</dt><dd>{me?.email}</dd></div>
            <div className="flex justify-between"><dt className="text-muted">Roles</dt><dd className="flex gap-1">{me?.roles.map((r) => <Badge key={r} tone="blue">{r}</Badge>)}</dd></div>
            <div className="flex justify-between"><dt className="text-muted">Last sign-in</dt><dd>{date(me?.last_login_at)}</dd></div>
          </dl>
          <div className="mt-5 flex items-center justify-between rounded-xl bg-[var(--surface-2)] p-4">
            <div className="flex items-start gap-3">
              <ShieldCheck className="mt-0.5 h-5 w-5 text-brand-600" />
              <div><p className="text-sm font-medium">Two-step verification</p><p className="text-xs text-muted">Ask for an emailed code every time you sign in.</p></div>
            </div>
            <Button size="sm" variant={me?.mfa_enabled ? "secondary" : "primary"} onClick={toggleMfa}>{me?.mfa_enabled ? "Turn off" : "Turn on"}</Button>
          </div>
        </Card>

        <Card title="Change password">
          <form className="space-y-4" onSubmit={async (e) => {
            e.preventDefault(); setErr("");
            if (pw.new_password !== pw.confirm) return setErr("New passwords do not match");
            setBusy(true);
            try { await api("/v1/auth/password", { body: { current_password: pw.current_password, new_password: pw.new_password } }); toast("Password changed; other sessions were signed out"); setPw({ current_password: "", new_password: "", confirm: "" }); }
            catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
          }}>
            {err && <Alert>{err}</Alert>}
            <Field label="Current password"><input className="input" type="password" autoComplete="current-password" value={pw.current_password} onChange={(e) => setPw({ ...pw, current_password: e.target.value })} /></Field>
            <Field label="New password"><input className="input" type="password" autoComplete="new-password" value={pw.new_password} onChange={(e) => setPw({ ...pw, new_password: e.target.value })} /></Field>
            <Field label="Confirm new password"><input className="input" type="password" autoComplete="new-password" value={pw.confirm} onChange={(e) => setPw({ ...pw, confirm: e.target.value })} /></Field>
            <Button loading={busy}>Update password</Button>
          </form>
        </Card>

        {profile.data && (
          <Card title="Contact details" className="lg:col-span-2">
            <form className="grid gap-4 sm:grid-cols-2" onSubmit={async (e) => {
              e.preventDefault();
              try { await api("/v1/customers/me", { method: "PATCH", body: edit }); toast("Profile updated"); setEdit({}); profile.reload(); }
              catch (e) { toast(errMsg(e), "red"); }
            }}>
              {(["phone", "address", "city", "occupation"] as const).map((k) => (
                <Field key={k} label={k[0].toUpperCase() + k.slice(1)}>
                  <input className="input" value={edit[k] ?? profile.data[k] ?? ""} onChange={(e) => setEdit({ ...edit, [k]: e.target.value })} />
                </Field>
              ))}
              <div className="sm:col-span-2 flex items-center justify-between">
                <p className="text-xs text-muted">CIF <span className="font-mono">{profile.data.cif}</span> · date of birth {profile.data.date_of_birth}</p>
                <Button disabled={Object.keys(edit).length === 0}>Save</Button>
              </div>
            </form>
          </Card>
        )}
      </div>
    </>
  );
}
