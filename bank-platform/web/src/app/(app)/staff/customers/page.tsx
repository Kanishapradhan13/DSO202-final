"use client";

import { useState } from "react";
import { ArrowDownToLine, ArrowUpFromLine, Lock, Search, Unlock } from "lucide-react";
import { Alert, Badge, Button, Card, cx, Empty, Field, Modal, Money, PageHeader, Spinner, Table, useToast } from "@/components/ui";
import { TxnList, Txn } from "@/components/txn-list";
import { useMe } from "@/components/me";
import { api, errMsg, useApi } from "@/lib/api";
import { date, hasRole, nu, parseNu } from "@/lib/format";
import type { Account } from "@/lib/types";

type Customer = { id: string; user_id: string; cif: string; full_name: string; email: string; phone: string; city: string; date_of_birth: string; created_at: string };

function CashModal({ kind, account, onClose, onDone }: { kind: "deposit" | "withdraw"; account: Account; onClose: () => void; onDone: () => void }) {
  const toast = useToast();
  const [amount, setAmount] = useState("");
  const [desc, setDesc] = useState("");
  const [key] = useState(() => crypto.randomUUID());
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const minor = parseNu(amount);
  return (
    <Modal open onClose={onClose} title={`${kind === "deposit" ? "Cash deposit" : "Cash withdrawal"} · ${account.account_number}`}>
      <form className="space-y-4" onSubmit={async (e) => {
        e.preventDefault(); setBusy(true); setErr("");
        try {
          const t = await api(`/v1/staff/transactions/${kind}`, { body: { account_id: account.id, amount: String(minor), description: desc }, idempotencyKey: key });
          toast(`${kind === "deposit" ? "Deposited" : "Withdrew"} ${nu(t.amount)}. New balance ${nu(t.balance_after)}`);
          onDone(); onClose();
        } catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
      }}>
        {err && <Alert>{err}</Alert>}
        <p className="text-sm text-muted">{account.holder_name} · available {nu(account.available_balance)}</p>
        <Field label="Amount (Nu.)"><input className="input text-lg num" inputMode="decimal" autoFocus value={amount} onChange={(e) => setAmount(e.target.value)} /></Field>
        <Field label="Narration"><input className="input" value={desc} onChange={(e) => setDesc(e.target.value)} placeholder={kind === "deposit" ? "Cash deposit at branch" : "Cash withdrawal at branch"} /></Field>
        <Button className="w-full" variant={kind === "deposit" ? "success" : "primary"} loading={busy} disabled={!minor}>
          {kind === "deposit" ? "Deposit" : "Withdraw"} {minor ? nu(minor) : ""}
        </Button>
      </form>
    </Modal>
  );
}

function CustomerPanel({ c }: { c: Customer }) {
  const { me } = useMe();
  const toast = useToast();
  const accounts = useApi<{ accounts: Account[] }>(`/v1/staff/accounts?user_id=${c.user_id}`, [c.user_id]);
  const txns = useApi<{ transactions: Txn[] }>(`/v1/staff/transactions?user_id=${c.user_id}&page_size=15`, [c.user_id]);
  const cards = useApi<{ cards: any[] }>(`/v1/staff/cards?user_id=${c.user_id}`, [c.user_id]);
  const [cash, setCash] = useState<{ kind: "deposit" | "withdraw"; account: Account } | null>(null);
  const teller = hasRole(me?.roles, "ops_officer");

  async function setStatus(a: Account, status: string) {
    const reason = status === "FROZEN" ? prompt("Reason for freezing this account?") : "";
    if (status === "FROZEN" && !reason) return;
    try { await api(`/v1/staff/accounts/${a.id}/status`, { body: { status, reason } }); toast(`Account ${status === "FROZEN" ? "frozen" : "reactivated"}`); accounts.reload(); }
    catch (e) { toast(errMsg(e), "red"); }
  }
  const reload = () => { accounts.reload(); txns.reload(); };

  return (
    <div className="space-y-6">
      <Card title={c.full_name} action={<span className="font-mono text-xs text-muted">{c.cif}</span>}>
        <dl className="grid gap-3 text-sm sm:grid-cols-4">
          {[["Email", c.email], ["Phone", c.phone || "—"], ["City", c.city], ["Customer since", date(c.created_at, false)]].map(([k, v]) => (
            <div key={k}><dt className="text-xs text-muted">{k}</dt><dd className="font-medium">{v}</dd></div>
          ))}
        </dl>
      </Card>
      <Card title="Accounts">
        {accounts.loading ? <Spinner /> : !accounts.data?.accounts?.length ? <Empty title="No accounts" /> : (
          <Table head={["Account", "Status", "Balance", "Available", ""]}>
            {accounts.data.accounts.map((a) => (
              <tr key={a.id}>
                <td className="px-5 py-3"><p className="font-mono">{a.account_number}</p><p className="text-xs text-muted">{a.type.toLowerCase()} · {a.nickname}</p></td>
                <td className="px-5 py-3"><Badge>{a.status}</Badge>{a.status_reason && <p className="mt-0.5 max-w-40 truncate text-xs text-muted">{a.status_reason}</p>}</td>
                <td className="px-5 py-3"><Money value={a.balance} /></td>
                <td className="px-5 py-3"><Money value={a.available_balance} /></td>
                <td className="whitespace-nowrap px-5 py-3 text-right">
                  {teller && a.status !== "CLOSED" && (
                    <div className="flex justify-end gap-1.5">
                      <Button size="sm" variant="secondary" onClick={() => setCash({ kind: "deposit", account: a })}><ArrowDownToLine className="h-3.5 w-3.5" />Deposit</Button>
                      <Button size="sm" variant="secondary" disabled={a.status !== "ACTIVE"} onClick={() => setCash({ kind: "withdraw", account: a })}><ArrowUpFromLine className="h-3.5 w-3.5" />Withdraw</Button>
                      {a.status === "ACTIVE"
                        ? <Button size="sm" variant="ghost" onClick={() => setStatus(a, "FROZEN")} title="Freeze"><Lock className="h-3.5 w-3.5" /></Button>
                        : <Button size="sm" variant="ghost" onClick={() => setStatus(a, "ACTIVE")} title="Unfreeze"><Unlock className="h-3.5 w-3.5" /></Button>}
                    </div>
                  )}
                </td>
              </tr>
            ))}
          </Table>
        )}
      </Card>
      <div className="grid gap-6 lg:grid-cols-[1.4fr_1fr]">
        <Card title="Recent transactions">{txns.loading ? <Spinner /> : <TxnList items={txns.data?.transactions} showAccount showBalance />}</Card>
        <Card title="Cards">
          {!cards.data?.cards?.length ? <p className="text-sm text-muted">No cards.</p> : (
            <ul className="space-y-2">{cards.data.cards.map((k) => (
              <li key={k.id} className="flex items-center justify-between rounded-xl bg-[var(--surface-2)] px-3 py-2 text-sm">
                <span className="font-mono">{k.masked_pan}</span><Badge>{k.status}</Badge>
              </li>
            ))}</ul>
          )}
        </Card>
      </div>
      {cash && <CashModal kind={cash.kind} account={cash.account} onClose={() => setCash(null)} onDone={reload} />}
    </div>
  );
}

export default function CustomersPage() {
  const [q, setQ] = useState("");
  const [query, setQuery] = useState("");
  const list = useApi<{ customers: Customer[] }>(`/v1/staff/customers?query=${encodeURIComponent(query)}&page_size=30`, [query]);
  const [sel, setSel] = useState<Customer | null>(null);

  return (
    <>
      <PageHeader title="Customers & teller" subtitle="Find a customer, then deposit, withdraw or freeze accounts." />
      <div className="grid gap-6 lg:grid-cols-[320px_1fr]">
        <Card>
          <form className="mb-4 flex gap-2" onSubmit={(e) => { e.preventDefault(); setQuery(q); }}>
            <input className="input" placeholder="Name, email, CIF or phone" value={q} onChange={(e) => setQ(e.target.value)} />
            <Button variant="secondary" size="sm" aria-label="Search"><Search className="h-4 w-4" /></Button>
          </form>
          {list.loading ? <Spinner /> : !list.data?.customers?.length ? <Empty title="No customers found" /> : (
            <ul className="-mx-5 -mb-5 max-h-[65vh] divide-y divide-[var(--border)] overflow-y-auto">
              {list.data.customers.map((c) => (
                <li key={c.id}>
                  <button onClick={() => setSel(c)} className={cx("w-full px-5 py-3 text-left hover:bg-[var(--surface-2)]", sel?.id === c.id && "bg-brand-50 dark:bg-brand-500/10")}>
                    <p className="text-sm font-medium">{c.full_name}</p>
                    <p className="text-xs text-muted">{c.cif} · {c.email}</p>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Card>
        {sel ? <CustomerPanel key={sel.id} c={sel} /> : <Card><Empty title="Select a customer">Search by name, email, CIF number or phone.</Empty></Card>}
      </div>
    </>
  );
}
