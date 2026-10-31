"use client";

import { useState } from "react";
import { Plus, Search } from "lucide-react";
import { Alert, Badge, Button, Card, Empty, Field, Modal, PageHeader, Spinner, Table, useToast } from "@/components/ui";
import { useMe } from "@/components/me";
import { api, errMsg, useApi } from "@/lib/api";
import { date } from "@/lib/format";

const ROLES = ["ops_officer", "loan_officer", "fraud_analyst", "auditor", "admin"];

export default function UsersPage() {
  const { me } = useMe();
  const toast = useToast();
  const [q, setQ] = useState("");
  const [query, setQuery] = useState("");
  const list = useApi<{ users: any[] }>(`/v1/admin/users?page_size=50&query=${encodeURIComponent(query)}`, [query]);
  const [create, setCreate] = useState(false);
  const [f, setF] = useState({ full_name: "", email: "", password: "", roles: ["ops_officer"] });
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  async function setStatus(u: any, status: string) {
    try { await api(`/v1/admin/users/${u.id}/status`, { body: { status } }); toast(`${u.email} ${status === "DISABLED" ? "disabled" : "enabled"}`); list.reload(); }
    catch (e) { toast(errMsg(e), "red"); }
  }

  return (
    <>
      <PageHeader title="Users & roles" subtitle="Create staff accounts and disable access. Disabling a user revokes their sessions."
        action={<Button onClick={() => { setErr(""); setCreate(true); }}><Plus className="h-4 w-4" />New staff user</Button>} />
      <Card>
        <form className="mb-4 flex max-w-md gap-2" onSubmit={(e) => { e.preventDefault(); setQuery(q); }}>
          <input className="input" placeholder="Search name or email" value={q} onChange={(e) => setQ(e.target.value)} />
          <Button variant="secondary" size="sm" aria-label="Search"><Search className="h-4 w-4" /></Button>
        </form>
        {list.loading ? <Spinner /> : !list.data?.users?.length ? <Empty title="No users" /> : (
          <Table head={["Name", "Email", "Roles", "MFA", "Status", "Last sign-in", ""]}>
            {list.data.users.map((u) => (
              <tr key={u.id}>
                <td className="px-5 py-3 font-medium">{u.full_name}</td>
                <td className="px-5 py-3">{u.email}</td>
                <td className="px-5 py-3"><div className="flex flex-wrap gap-1">{u.roles.map((r: string) => <Badge key={r} tone={r === "customer" ? "gray" : "blue"}>{r}</Badge>)}</div></td>
                <td className="px-5 py-3">{u.mfa_enabled ? "On" : "Off"}</td>
                <td className="px-5 py-3"><Badge>{u.status}</Badge></td>
                <td className="px-5 py-3 text-muted">{date(u.last_login_at)}</td>
                <td className="px-5 py-3 text-right">
                  {u.id !== me?.id && (u.status === "DISABLED"
                    ? <Button size="sm" variant="secondary" onClick={() => setStatus(u, "ACTIVE")}>Enable</Button>
                    : <Button size="sm" variant="ghost" onClick={() => setStatus(u, "DISABLED")}>Disable</Button>)}
                </td>
              </tr>
            ))}
          </Table>
        )}
      </Card>
      <Modal open={create} onClose={() => setCreate(false)} title="New staff user">
        <form className="space-y-4" onSubmit={async (e) => {
          e.preventDefault(); setBusy(true); setErr("");
          try { await api("/v1/admin/users", { body: f }); toast("Staff user created"); setCreate(false); list.reload(); setF({ full_name: "", email: "", password: "", roles: ["ops_officer"] }); }
          catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
        }}>
          {err && <Alert>{err}</Alert>}
          <Field label="Full name"><input className="input" required value={f.full_name} onChange={(e) => setF({ ...f, full_name: e.target.value })} /></Field>
          <Field label="Email"><input className="input" type="email" required value={f.email} onChange={(e) => setF({ ...f, email: e.target.value })} /></Field>
          <Field label="Temporary password"><input className="input" type="password" required minLength={8} value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} /></Field>
          <fieldset>
            <legend className="label">Roles</legend>
            <div className="flex flex-wrap gap-3">
              {ROLES.map((r) => (
                <label key={r} className="flex items-center gap-1.5 text-sm">
                  <input type="checkbox" checked={f.roles.includes(r)} onChange={(e) => setF({ ...f, roles: e.target.checked ? [...f.roles, r] : f.roles.filter((x) => x !== r) })} />{r.replace("_", " ")}
                </label>
              ))}
            </div>
          </fieldset>
          <Button className="w-full" loading={busy} disabled={!f.roles.length}>Create user</Button>
        </form>
      </Modal>
    </>
  );
}
