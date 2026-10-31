"use client";

import { useState } from "react";
import { CreditCard, Lock, Plus, ShoppingBag, SlidersHorizontal, Unlock, Wifi } from "lucide-react";
import { Alert, Badge, Button, Card, cx, Empty, Field, Modal, Money, PageHeader, Spinner, Table, useToast } from "@/components/ui";
import { api, errMsg, useApi } from "@/lib/api";
import { date, n, nu, parseNu } from "@/lib/format";
import type { Account } from "@/lib/types";

type CardT = {
  id: string; account_id: string; account_number: string; masked_pan: string; last4: string; expiry_month: number; expiry_year: number;
  cardholder_name: string; status: string; block_reason: string; daily_limit: string; per_transaction_limit: string; online_enabled: boolean; spent_today: string; network: string;
};

function CardFace({ c }: { c: CardT }) {
  const dim = c.status !== "ACTIVE";
  return (
    <div className={cx("relative aspect-[1.586] w-full max-w-sm overflow-hidden rounded-2xl p-5 text-white shadow-lg", dim ? "bg-gradient-to-br from-slate-600 to-slate-800" : "bg-gradient-to-br from-brand-900 via-brand-700 to-brand-500")}>
      <div className="absolute -right-10 -top-10 h-40 w-40 rounded-full bg-white/10" />
      <div className="flex items-center justify-between">
        <span className="font-semibold">CBMP <span className="text-saffron-400">Bank</span></span>
        <Wifi className="h-5 w-5 rotate-90 opacity-80" />
      </div>
      <div className="mt-6 h-8 w-11 rounded-md bg-gradient-to-br from-saffron-400 to-amber-600" />
      <p className="mt-4 font-mono text-lg tracking-widest">{c.masked_pan}</p>
      <div className="mt-3 flex items-end justify-between text-xs">
        <div><p className="opacity-60">CARDHOLDER</p><p className="font-medium tracking-wide">{c.cardholder_name}</p></div>
        <div><p className="opacity-60">EXPIRES</p><p className="font-mono">{String(c.expiry_month).padStart(2, "0")}/{String(c.expiry_year).slice(2)}</p></div>
        <p className="text-sm font-bold italic">VISA</p>
      </div>
      {dim && <div className="absolute inset-0 flex items-center justify-center bg-slate-950/40"><Badge>{c.status}</Badge></div>}
    </div>
  );
}

function CardPanel({ c, reload }: { c: CardT; reload: () => void }) {
  const toast = useToast();
  const auths = useApi<{ authorizations: any[] }>(`/v1/cards/${c.id}/authorizations`);
  const [modal, setModal] = useState<"" | "activate" | "limits" | "block" | "buy">("");
  const [pin, setPin] = useState("");
  const [limits, setLimits] = useState({ daily: nu(c.daily_limit).replace("Nu. ", ""), per: nu(c.per_transaction_limit).replace("Nu. ", ""), online: c.online_enabled });
  const [buy, setBuy] = useState({ merchant: "Norzin Lam Supermarket", amount: "", channel: "POS", pin: "" });
  const [reason, setReason] = useState("TEMPORARY");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  async function run(fn: () => Promise<any>, msg?: string) {
    setBusy(true); setErr("");
    try { const r = await fn(); if (msg) toast(msg); setModal(""); reload(); auths.reload(); return r; }
    catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
  }

  const usedPct = Math.min(100, (n(c.spent_today) / Math.max(1, n(c.daily_limit))) * 100);

  return (
    <Card>
      <div className="grid gap-6 md:grid-cols-[minmax(0,22rem)_1fr]">
        <CardFace c={c} />
        <div className="space-y-4">
          <div className="flex flex-wrap items-center gap-2">
            <Badge>{c.status}</Badge>
            <span className="text-sm text-muted">Linked to <span className="font-mono">{c.account_number}</span></span>
            {c.block_reason && <span className="text-xs text-muted">({c.block_reason.toLowerCase().replace(/_/g, " ")})</span>}
          </div>
          <div>
            <div className="flex justify-between text-sm"><span className="text-muted">Spent today</span><span className="num"><Money value={c.spent_today} /> of <Money value={c.daily_limit} /></span></div>
            <div className="mt-1.5 h-2 overflow-hidden rounded-full bg-[var(--surface-2)]"><div className="h-full rounded-full bg-brand-500" style={{ width: `${usedPct}%` }} /></div>
            <p className="mt-1.5 text-xs text-muted">Per transaction up to <Money value={c.per_transaction_limit} /> · online payments {c.online_enabled ? "on" : "off"}</p>
          </div>
          <div className="flex flex-wrap gap-2">
            {c.status === "INACTIVE" && <Button size="sm" onClick={() => { setPin(""); setErr(""); setModal("activate"); }}>Activate & set PIN</Button>}
            {c.status === "ACTIVE" && <Button size="sm" onClick={() => { setErr(""); setModal("buy"); }}><ShoppingBag className="h-4 w-4" />Simulate purchase</Button>}
            {(c.status === "ACTIVE" || c.status === "INACTIVE") && <Button size="sm" variant="secondary" onClick={() => { setErr(""); setModal("block"); }}><Lock className="h-4 w-4" />Block</Button>}
            {c.status === "BLOCKED" && <Button size="sm" variant="secondary" onClick={() => run(() => api(`/v1/cards/${c.id}/unblock`, { body: {} }), "Card unblocked")}><Unlock className="h-4 w-4" />Unblock</Button>}
            {c.status !== "LOST" && <Button size="sm" variant="secondary" onClick={() => { setErr(""); setModal("limits"); }}><SlidersHorizontal className="h-4 w-4" />Limits</Button>}
          </div>
        </div>
      </div>

      <div className="mt-6">
        <p className="mb-2 text-xs font-semibold uppercase tracking-wide text-muted">Recent authorisations</p>
        {!auths.data?.authorizations?.length ? <p className="text-sm text-muted">No card activity yet.</p> : (
          <Table head={["Merchant", "Channel", "Amount", "Result", "When"]}>
            {auths.data.authorizations.map((a) => (
              <tr key={a.id}>
                <td className="px-5 py-2.5 font-medium">{a.merchant}</td>
                <td className="px-5 py-2.5 text-muted">{a.channel}</td>
                <td className="px-5 py-2.5"><Money value={a.amount} /></td>
                <td className="px-5 py-2.5"><Badge>{a.status}</Badge>{a.decline_reason && <p className="mt-0.5 text-xs text-muted">{a.decline_reason}</p>}</td>
                <td className="px-5 py-2.5 text-muted">{date(a.created_at)}</td>
              </tr>
            ))}
          </Table>
        )}
      </div>

      <Modal open={modal === "activate"} onClose={() => setModal("")} title="Activate card">
        <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); run(() => api(`/v1/cards/${c.id}/activate`, { body: { pin } }), "Card activated"); }}>
          {err && <Alert>{err}</Alert>}
          <Field label="Choose a 4-digit PIN" hint="Avoid obvious PINs like 1234.">
            <input className="input text-center font-mono text-xl tracking-[0.6em]" type="password" inputMode="numeric" maxLength={4} value={pin} onChange={(e) => setPin(e.target.value.replace(/\D/g, ""))} />
          </Field>
          <Button className="w-full" loading={busy} disabled={pin.length !== 4}>Activate</Button>
        </form>
      </Modal>

      <Modal open={modal === "limits"} onClose={() => setModal("")} title="Card limits">
        <form className="space-y-4" onSubmit={(e) => {
          e.preventDefault();
          run(() => api(`/v1/cards/${c.id}/limits`, { body: { daily_limit: String(parseNu(limits.daily)), per_transaction_limit: String(parseNu(limits.per)), online_enabled: limits.online } }), "Limits updated");
        }}>
          {err && <Alert>{err}</Alert>}
          <Field label="Daily limit (Nu.)" hint="Up to Nu. 200,000"><input className="input num" value={limits.daily} onChange={(e) => setLimits({ ...limits, daily: e.target.value })} /></Field>
          <Field label="Per-transaction limit (Nu.)"><input className="input num" value={limits.per} onChange={(e) => setLimits({ ...limits, per: e.target.value })} /></Field>
          <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={limits.online} onChange={(e) => setLimits({ ...limits, online: e.target.checked })} />Allow online payments</label>
          <Button className="w-full" loading={busy}>Save limits</Button>
        </form>
      </Modal>

      <Modal open={modal === "block"} onClose={() => setModal("")} title="Block card">
        <div className="space-y-4">
          {err && <Alert>{err}</Alert>}
          {[["TEMPORARY", "Temporarily", "You can unblock it later."], ["LOST", "Lost", "Permanent. Order a new card."], ["STOLEN", "Stolen", "Permanent. We'll flag it."]].map(([v, t, d]) => (
            <button key={v} type="button" onClick={() => setReason(v)} className={cx("w-full rounded-xl border p-3 text-left", reason === v ? "border-red-400 bg-red-50 dark:bg-red-500/10" : "border-base")}>
              <p className="text-sm font-medium">{t}</p><p className="text-xs text-muted">{d}</p>
            </button>
          ))}
          <Button variant="danger" className="w-full" loading={busy} onClick={() => run(() => api(`/v1/cards/${c.id}/block`, { body: { reason } }), "Card blocked")}>Block card</Button>
        </div>
      </Modal>

      <Modal open={modal === "buy"} onClose={() => setModal("")} title="Simulate a card purchase">
        <form className="space-y-4" onSubmit={async (e) => {
          e.preventDefault();
          const r = await run(() => api(`/v1/cards/${c.id}/simulate-purchase`, { body: { merchant: buy.merchant, amount: String(parseNu(buy.amount)), channel: buy.channel, pin: buy.pin }, idempotent: true }));
          if (r) toast(r.status === "APPROVED" ? `Approved: ${nu(r.amount)} at ${r.merchant}` : `Declined: ${r.decline_reason}`, r.status === "APPROVED" ? "green" : "red");
        }}>
          {err && <Alert>{err}</Alert>}
          <Field label="Merchant"><input className="input" value={buy.merchant} onChange={(e) => setBuy({ ...buy, merchant: e.target.value })} /></Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Amount (Nu.)"><input className="input num" inputMode="decimal" value={buy.amount} onChange={(e) => setBuy({ ...buy, amount: e.target.value })} /></Field>
            <Field label="Channel">
              <select className="input" value={buy.channel} onChange={(e) => setBuy({ ...buy, channel: e.target.value })}><option value="POS">In store (PIN)</option><option value="ONLINE">Online</option></select>
            </Field>
          </div>
          {buy.channel === "POS" && <Field label="PIN"><input className="input font-mono tracking-[0.4em]" type="password" inputMode="numeric" maxLength={4} value={buy.pin} onChange={(e) => setBuy({ ...buy, pin: e.target.value.replace(/\D/g, "") })} /></Field>}
          <Button className="w-full" loading={busy} disabled={!parseNu(buy.amount)}>Authorise payment</Button>
        </form>
      </Modal>
    </Card>
  );
}

export default function CardsPage() {
  const toast = useToast();
  const cards = useApi<{ cards: CardT[] }>("/v1/cards");
  const accounts = useApi<{ accounts: Account[] }>("/v1/accounts");
  const [issue, setIssue] = useState(false);
  const [acct, setAcct] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const withCard = new Set((cards.data?.cards ?? []).filter((c) => c.status !== "LOST").map((c) => c.account_id));
  const eligible = (accounts.data?.accounts ?? []).filter((a) => a.status === "ACTIVE" && !withCard.has(a.id));

  return (
    <>
      <PageHeader title="Cards" subtitle="Debit cards draw directly on your account's available balance."
        action={eligible.length > 0 && <Button onClick={() => { setAcct(eligible[0].id); setErr(""); setIssue(true); }}><Plus className="h-4 w-4" />Get a debit card</Button>} />
      {cards.loading ? <Spinner /> : !cards.data?.cards?.length ? (
        <Card><Empty title="No cards yet" icon={<CreditCard className="h-5 w-5" />}>Issue a free virtual debit card for any of your accounts.</Empty></Card>
      ) : (
        <div className="space-y-6">{cards.data.cards.map((c) => <CardPanel key={c.id} c={c} reload={cards.reload} />)}</div>
      )}
      <Modal open={issue} onClose={() => setIssue(false)} title="Get a debit card">
        <form className="space-y-4" onSubmit={async (e) => {
          e.preventDefault(); setBusy(true); setErr("");
          try { const c = await api("/v1/cards", { body: { account_id: acct } }); toast(`Card ending ${c.last4} issued`); setIssue(false); cards.reload(); }
          catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
        }}>
          {err && <Alert>{err}</Alert>}
          <Field label="Link to account">
            <select className="input" value={acct} onChange={(e) => setAcct(e.target.value)}>
              {eligible.map((a) => <option key={a.id} value={a.id}>{a.nickname} · {a.account_number}</option>)}
            </select>
          </Field>
          <p className="text-xs text-muted">Card numbers are tokenised: the bank stores only a token, a hash and the last four digits.</p>
          <Button className="w-full" loading={busy}>Issue card</Button>
        </form>
      </Modal>
    </>
  );
}
