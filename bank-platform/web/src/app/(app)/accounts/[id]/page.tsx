"use client";

import { use, useEffect, useState } from "react";
import Link from "next/link";
import { ArrowLeft, Copy, Download, FileText, ShieldCheck, ShieldX } from "lucide-react";
import { Alert, Badge, Button, Card, cx, Empty, Field, Money, PageHeader, Spinner, Stat, Table, useToast } from "@/components/ui";
import { TxnList, Txn } from "@/components/txn-list";
import { api, errMsg, useApi } from "@/lib/api";
import { date, n, nu, parseNu, today } from "@/lib/format";
import type { Account } from "@/lib/types";

const TYPES = ["", "DEPOSIT", "WITHDRAWAL", "TRANSFER_IN", "TRANSFER_OUT", "PAYMENT", "CARD_PURCHASE", "LOAN_DISBURSEMENT", "LOAN_REPAYMENT"];

function History({ accountId }: { accountId: string }) {
  const [filters, setFilters] = useState({ type: "", direction: "", from: "", to: "", query: "", min: "" });
  const [items, setItems] = useState<Txn[]>([]);
  const [token, setToken] = useState("");
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState("");

  async function load(reset: boolean) {
    setLoading(true);
    const q = new URLSearchParams({ page_size: "20" });
    if (filters.type) q.set("type", filters.type);
    if (filters.direction) q.set("direction", filters.direction);
    if (filters.from) q.set("from", filters.from);
    if (filters.to) q.set("to", filters.to);
    if (filters.query) q.set("query", filters.query);
    const min = parseNu(filters.min);
    if (min) q.set("min_amount", String(min));
    if (!reset && token) q.set("page_token", token);
    try {
      const r = await api<{ transactions: Txn[]; next_page_token: string }>(`/v1/accounts/${accountId}/transactions?${q}`);
      setItems(reset ? r.transactions : [...items, ...r.transactions]);
      setToken(r.next_page_token);
      setErr("");
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setLoading(false);
    }
  }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { load(true); }, [accountId]);

  const set = (k: keyof typeof filters) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => setFilters({ ...filters, [k]: e.target.value });
  return (
    <Card title="Transaction history">
      <form className="mb-4 grid gap-3 sm:grid-cols-3 lg:grid-cols-6" onSubmit={(e) => { e.preventDefault(); load(true); }}>
        <select className="input" value={filters.type} onChange={set("type")} aria-label="Type">
          {TYPES.map((t) => <option key={t} value={t}>{t ? t.replace(/_/g, " ").toLowerCase() : "All types"}</option>)}
        </select>
        <select className="input" value={filters.direction} onChange={set("direction")} aria-label="Direction">
          <option value="">In &amp; out</option><option value="CREDIT">Money in</option><option value="DEBIT">Money out</option>
        </select>
        <input className="input" type="date" value={filters.from} onChange={set("from")} aria-label="From" />
        <input className="input" type="date" value={filters.to} onChange={set("to")} aria-label="To" />
        <input className="input" placeholder="Min Nu." value={filters.min} onChange={set("min")} aria-label="Minimum amount" />
        <div className="flex gap-2">
          <input className="input" placeholder="Search" value={filters.query} onChange={set("query")} />
          <Button variant="secondary" size="sm">Go</Button>
        </div>
      </form>
      {err && <Alert>{err}</Alert>}
      {loading && items.length === 0 ? <Spinner /> : <TxnList items={items} showBalance />}
      {token && <div className="mt-4 text-center"><Button variant="secondary" size="sm" loading={loading} onClick={() => load(false)}>Load more</Button></div>}
    </Card>
  );
}

function Statements({ accountId }: { accountId: string }) {
  const toast = useToast();
  const { data, reload, loading } = useApi<{ statements: any[] }>(`/v1/accounts/${accountId}/statements`);
  const [from, setFrom] = useState(today(-30));
  const [to, setTo] = useState(today());
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  return (
    <Card title="Statements">
      <form className="mb-5 flex flex-wrap items-end gap-3" onSubmit={async (e) => {
        e.preventDefault(); setBusy(true); setErr("");
        try { await api(`/v1/accounts/${accountId}/statements`, { body: { from, to } }); toast("Statement generated"); reload(); }
        catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
      }}>
        <Field label="From"><input className="input" type="date" value={from} onChange={(e) => setFrom(e.target.value)} /></Field>
        <Field label="To"><input className="input" type="date" value={to} onChange={(e) => setTo(e.target.value)} /></Field>
        <Button loading={busy}><FileText className="h-4 w-4" />Generate</Button>
      </form>
      {err && <div className="mb-3"><Alert>{err}</Alert></div>}
      {loading ? <Spinner /> : !data?.statements?.length ? <Empty title="No statements yet">Generate one for any period up to a year.</Empty> : (
        <Table head={["Period", "Opening", "In", "Out", "Closing", "Ledger check", ""]}>
          {data.statements.map((s) => (
            <tr key={s.id}>
              <td className="px-5 py-3">{s.period_from} → {s.period_to}<div className="text-xs text-muted">{s.kind === "MONTHLY" ? "Monthly" : "On demand"} · {s.entry_count} entries</div></td>
              <td className="px-5 py-3"><Money value={s.opening_balance} /></td>
              <td className="px-5 py-3 text-emerald-600"><Money value={s.total_credits} /></td>
              <td className="px-5 py-3"><Money value={s.total_debits} /></td>
              <td className="px-5 py-3 font-medium"><Money value={s.closing_balance} /></td>
              <td className="px-5 py-3">{s.reconciled
                ? <span className="inline-flex items-center gap-1 text-xs text-emerald-600"><ShieldCheck className="h-3.5 w-3.5" />Reconciled</span>
                : <span className="inline-flex items-center gap-1 text-xs text-red-600"><ShieldX className="h-3.5 w-3.5" />Mismatch</span>}</td>
              <td className="whitespace-nowrap px-5 py-3 text-right">
                <a className="mr-3 inline-flex items-center gap-1 text-sm font-medium text-brand-600" href={`/api/gw/v1/statements/${s.id}/download?format=pdf`}><Download className="h-3.5 w-3.5" />PDF</a>
                <a className="inline-flex items-center gap-1 text-sm font-medium text-brand-600" href={`/api/gw/v1/statements/${s.id}/download?format=csv`}>CSV</a>
              </td>
            </tr>
          ))}
        </Table>
      )}
    </Card>
  );
}

export default function AccountPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  const toast = useToast();
  const { data: a, loading, error, reload } = useApi<Account>(`/v1/accounts/${id}`);
  const [tab, setTab] = useState<"history" | "statements">("history");
  if (loading) return <Spinner />;
  if (error || !a) return <Alert>{error?.message ?? "Account not found"}</Alert>;

  async function close() {
    if (!confirm("Close this account? This cannot be undone.")) return;
    try { await api(`/v1/accounts/${id}/close`, { body: {} }); toast("Account closed"); reload(); }
    catch (e) { toast(errMsg(e), "red"); }
  }

  return (
    <>
      <Link href="/accounts" className="mb-4 inline-flex items-center gap-1 text-sm text-muted hover:text-[var(--text)]"><ArrowLeft className="h-4 w-4" />Accounts</Link>
      <PageHeader
        title={a.nickname}
        subtitle={
          <span className="inline-flex items-center gap-2">
            <span className="font-mono">{a.account_number}</span>
            <button onClick={() => { navigator.clipboard.writeText(a.account_number); toast("Account number copied", "blue"); }} aria-label="Copy account number"><Copy className="h-3.5 w-3.5" /></button>
            · {a.type.toLowerCase()} · {a.holder_name}
          </span>
        }
        action={<div className="flex items-center gap-2"><Badge>{a.status}</Badge>
          {a.status !== "CLOSED" && n(a.balance) === 0 && <Button variant="secondary" size="sm" onClick={close}>Close account</Button>}</div>}
      />
      {a.status === "FROZEN" && <div className="mb-4"><Alert>This account is frozen: {a.status_reason}. Incoming money is still accepted; outgoing payments are blocked.</Alert></div>}
      <div className="grid gap-4 sm:grid-cols-4">
        <Stat label="Available" value={nu(a.available_balance)} />
        <Stat label="Ledger balance" value={nu(a.balance)} />
        <Stat label="On hold" value={nu(a.held_amount)} hint="Reserved by pending transactions" />
        <Stat label="Daily transfer limit" value={nu(a.daily_transfer_limit)} />
      </div>
      <div className="mb-4 mt-6 flex gap-1 rounded-xl bg-[var(--surface-2)] p-1 text-sm font-medium sm:w-fit">
        {(["history", "statements"] as const).map((t) => (
          <button key={t} onClick={() => setTab(t)} className={cx("flex-1 rounded-lg px-4 py-1.5 capitalize", tab === t ? "surface shadow-sm" : "text-muted")}>{t}</button>
        ))}
      </div>
      {tab === "history" ? <History accountId={id} /> : <Statements accountId={id} />}
    </>
  );
}
