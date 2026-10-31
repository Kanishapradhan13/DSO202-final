"use client";

import { Fragment, useState } from "react";
import { Link2, ShieldCheck, ShieldX } from "lucide-react";
import { Alert, Badge, Button, Card, Empty, Money, PageHeader, Spinner, Table, useToast } from "@/components/ui";
import { useMe } from "@/components/me";
import { api, errMsg, useApi } from "@/lib/api";
import { date, hasRole } from "@/lib/format";

function TrialBalance() {
  const { data, loading, reload } = useApi<any>("/v1/staff/ledger/trial-balance");
  if (loading) return <Card title="Trial balance"><Spinner /></Card>;
  if (!data) return null;
  return (
    <Card title="Trial balance" action={<Button size="sm" variant="secondary" onClick={reload}>Recheck</Button>}>
      <div className="flex items-center gap-3">
        {data.balanced ? <ShieldCheck className="h-8 w-8 text-emerald-600" /> : <ShieldX className="h-8 w-8 text-red-600" />}
        <div>
          <p className="font-semibold">{data.balanced ? "Ledger balanced" : "Ledger integrity failure"}</p>
          <p className="text-sm text-muted">{Number(data.entry_count)} entries · {Number(data.unbalanced_entries)} unbalanced · {Number(data.balance_mismatches)} balance mismatches</p>
        </div>
      </div>
      <div className="mt-4 grid grid-cols-2 gap-3 text-sm">
        <div className="rounded-xl bg-[var(--surface-2)] p-3"><p className="text-xs text-muted">Total debits</p><Money value={data.total_debits} className="font-semibold" /></div>
        <div className="rounded-xl bg-[var(--surface-2)] p-3"><p className="text-xs text-muted">Total credits</p><Money value={data.total_credits} className="font-semibold" /></div>
      </div>
      <p className="mb-2 mt-5 text-xs font-semibold uppercase tracking-wide text-muted">System accounts (credits − debits)</p>
      <ul className="space-y-1 text-sm">
        {data.system_accounts?.map((a: any) => (
          <li key={a.account_id} className="flex justify-between"><span>{a.name} <span className="font-mono text-xs text-muted">{a.account_id}</span></span><Money value={a.balance} /></li>
        ))}
        <li className="flex justify-between border-t border-base pt-1 font-medium"><span>Customer deposits</span><Money value={data.customer_funds} /></li>
      </ul>
    </Card>
  );
}

export default function AuditPage() {
  const { me } = useMe();
  const toast = useToast();
  const [f, setF] = useState({ service: "", action: "", actor_id: "", resource_id: "", request_id: "" });
  const [query, setQuery] = useState("");
  const [page, setPage] = useState("");
  const events = useApi<{ events: any[]; total: string; next_page_token: string }>(`/v1/staff/audit/events?page_size=30${query}${page ? `&page_token=${page}` : ""}`, [query, page]);
  const [open, setOpen] = useState<string | null>(null);
  const [chain, setChain] = useState<any>(null);
  const auditor = hasRole(me?.roles, "auditor");

  async function verify() {
    try { setChain(await api("/v1/staff/audit/verify")); } catch (e) { toast(errMsg(e), "red"); }
  }

  const search = (e: React.FormEvent) => {
    e.preventDefault();
    const q = Object.entries(f).filter(([, v]) => v.trim()).map(([k, v]) => `&${k}=${encodeURIComponent(v.trim())}`).join("");
    setPage(""); setQuery(q);
  };

  return (
    <>
      <PageHeader title="Audit & ledger" subtitle="Append-only, hash-chained audit trail and live ledger integrity."
        action={auditor && <Button variant="secondary" onClick={verify}><Link2 className="h-4 w-4" />Verify hash chain</Button>} />
      {chain && (
        <div className="mb-6">
          <Alert tone={chain.valid ? "green" : "red"}>
            {chain.valid
              ? <>Chain intact: {Number(chain.checked).toLocaleString()} records verified. Head <span className="font-mono text-xs">{chain.head_hash.slice(0, 16)}…</span></>
              : <>Chain broken at sequence {chain.first_broken_sequence}. Records may have been altered.</>}
          </Alert>
        </div>
      )}
      <div className="grid gap-6 xl:grid-cols-[1fr_360px]">
        <Card title={`Audit events${events.data ? ` · ${Number(events.data.total).toLocaleString()}` : ""}`}>
          <form onSubmit={search} className="mb-4 grid gap-2 sm:grid-cols-3 lg:grid-cols-6">
            <select className="input" value={f.service} onChange={(e) => setF({ ...f, service: e.target.value })} aria-label="Service">
              <option value="">All services</option>
              {["auth", "customer", "kyc", "account", "transaction", "transfer", "payment", "card", "loan", "fraud", "statement"].map((s) => <option key={s} value={`${s}-service`}>{s}</option>)}
            </select>
            <input className="input" placeholder="Action prefix" value={f.action} onChange={(e) => setF({ ...f, action: e.target.value })} />
            <input className="input" placeholder="Actor ID" value={f.actor_id} onChange={(e) => setF({ ...f, actor_id: e.target.value })} />
            <input className="input" placeholder="Resource ID" value={f.resource_id} onChange={(e) => setF({ ...f, resource_id: e.target.value })} />
            <input className="input" placeholder="Request ID" value={f.request_id} onChange={(e) => setF({ ...f, request_id: e.target.value })} />
            <Button variant="secondary">Filter</Button>
          </form>
          {events.loading ? <Spinner /> : !events.data?.events?.length ? <Empty title="No events" /> : (
            <Table head={["#", "When", "Service", "Action", "Actor", "Outcome"]}>
              {events.data.events.map((e) => (
                <Fragment key={e.id}>
                  <tr className="cursor-pointer hover:bg-[var(--surface-2)]" onClick={() => setOpen(open === e.id ? null : e.id)}>
                    <td className="px-5 py-2.5 font-mono text-xs text-muted">{e.sequence}</td>
                    <td className="whitespace-nowrap px-5 py-2.5 text-muted">{date(e.occurred_at)}</td>
                    <td className="px-5 py-2.5">{e.service.replace("-service", "")}</td>
                    <td className="px-5 py-2.5 font-mono text-xs">{e.action}</td>
                    <td className="max-w-[10rem] truncate px-5 py-2.5 font-mono text-xs">{e.actor_id}</td>
                    <td className="px-5 py-2.5"><Badge>{e.outcome}</Badge></td>
                  </tr>
                  {open === e.id && (
                    <tr><td colSpan={6} className="bg-[var(--surface-2)] px-5 py-3">
                      <div className="grid gap-3 text-xs md:grid-cols-2">
                        <div><p className="mb-1 font-semibold text-muted">Before</p><pre className="overflow-x-auto whitespace-pre-wrap break-all font-mono">{e.before_json || "—"}</pre></div>
                        <div><p className="mb-1 font-semibold text-muted">After</p><pre className="overflow-x-auto whitespace-pre-wrap break-all font-mono">{e.after_json || "—"}</pre></div>
                        <p className="break-all text-muted md:col-span-2">{e.resource_type}:{e.resource_id} · request {e.request_id || "—"} · roles {e.actor_roles || "—"}<br />hash {e.hash}<br />prev {e.prev_hash}</p>
                      </div>
                    </td></tr>
                  )}
                </Fragment>
              ))}
            </Table>
          )}
          <div className="mt-4 flex justify-between">
            <Button size="sm" variant="secondary" disabled={!page} onClick={() => setPage("")}>First page</Button>
            <Button size="sm" variant="secondary" disabled={!events.data?.next_page_token} onClick={() => setPage(events.data!.next_page_token)}>Next page</Button>
          </div>
        </Card>
        {auditor && <TrialBalance />}
      </div>
    </>
  );
}
