"use client";

import { useEffect, useMemo, useState } from "react";
import { ArrowRight, BookUser, CheckCircle2, Clock, Trash2, UserPlus, XCircle } from "lucide-react";
import { Alert, Badge, Button, Card, cx, Empty, Field, Modal, Money, PageHeader, Spinner, Table, useToast } from "@/components/ui";
import { SagaTimeline } from "@/components/saga";
import { api, errMsg, useApi } from "@/lib/api";
import { date, n, nu, parseNu } from "@/lib/format";
import type { Account } from "@/lib/types";

type Payee = { id: string; nickname: string; account_number: string; holder_name: string };
type Transfer = any;

function useLookup(num: string) {
  const [res, setRes] = useState<{ holder_name?: string; error?: string; loading?: boolean }>({});
  useEffect(() => {
    if (!/^\d{12}$/.test(num)) { setRes({}); return; }
    setRes({ loading: true });
    const t = setTimeout(async () => {
      try {
        const r = await api(`/v1/accounts/lookup/${num}`);
        setRes({ holder_name: r.holder_name });
      } catch (e) { setRes({ error: errMsg(e) }); }
    }, 350);
    return () => clearTimeout(t);
  }, [num]);
  return res;
}

function PayeesModal({ open, onClose, payees, reload }: { open: boolean; onClose: () => void; payees: Payee[]; reload: () => void }) {
  const toast = useToast();
  const [num, setNum] = useState("");
  const [nick, setNick] = useState("");
  const [busy, setBusy] = useState(false);
  const look = useLookup(num);
  return (
    <Modal open={open} onClose={onClose} title="Saved payees">
      <form className="mb-5 space-y-3" onSubmit={async (e) => {
        e.preventDefault(); setBusy(true);
        try { await api("/v1/payees", { body: { account_number: num, nickname: nick } }); setNum(""); setNick(""); reload(); toast("Payee saved"); }
        catch (e) { toast(errMsg(e), "red"); } finally { setBusy(false); }
      }}>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Account number" hint={look.holder_name ? `✓ ${look.holder_name}` : look.error}>
            <input className="input font-mono" inputMode="numeric" maxLength={12} value={num} onChange={(e) => setNum(e.target.value.replace(/\D/g, ""))} />
          </Field>
          <Field label="Nickname"><input className="input" value={nick} onChange={(e) => setNick(e.target.value)} placeholder="e.g. Mum" /></Field>
        </div>
        <Button size="sm" loading={busy} disabled={!look.holder_name}><UserPlus className="h-4 w-4" />Add payee</Button>
      </form>
      {payees.length === 0 ? <p className="text-sm text-muted">No saved payees.</p> : (
        <ul className="divide-y divide-[var(--border)]">
          {payees.map((p) => (
            <li key={p.id} className="flex items-center justify-between py-2.5">
              <div><p className="text-sm font-medium">{p.nickname}</p><p className="font-mono text-xs text-muted">{p.account_number} · {p.holder_name}</p></div>
              <button className="rounded-lg p-2 text-muted hover:text-red-600" aria-label="Remove payee"
                onClick={async () => { await api(`/v1/payees/${p.id}`, { method: "DELETE" }); reload(); }}>
                <Trash2 className="h-4 w-4" />
              </button>
            </li>
          ))}
        </ul>
      )}
    </Modal>
  );
}

function ResultView({ t, onNew }: { t: Transfer; onNew: () => void }) {
  const ok = t.status === "COMPLETED";
  const review = t.status === "PENDING_REVIEW";
  const Icon = ok ? CheckCircle2 : review ? Clock : XCircle;
  return (
    <Card>
      <div className="flex flex-col items-center text-center">
        <Icon className={cx("h-12 w-12", ok ? "text-emerald-600" : review ? "text-amber-500" : "text-red-600")} />
        <h2 className="mt-3 text-xl font-semibold">
          {ok ? "Transfer complete" : review ? "Held for security review" : t.status === "REJECTED" ? "Transfer declined" : "Transfer failed"}
        </h2>
        <p className="mt-1 text-3xl font-semibold num">{nu(t.amount)}</p>
        <p className="mt-1 text-sm text-muted">to {t.to_name} · <span className="font-mono">{t.to_account_number}</span></p>
        {t.failure_reason && <div className="mt-4 w-full max-w-md"><Alert>{t.failure_reason}</Alert></div>}
        {review && <div className="mt-4 w-full max-w-md"><Alert tone="amber">Funds are reserved while a fraud analyst reviews this transfer. You'll get a notification when it's decided.</Alert></div>}
      </div>
      <div className="mx-auto mt-6 max-w-md">
        <p className="mb-3 text-xs font-semibold uppercase tracking-wide text-muted">Saga steps</p>
        <SagaTimeline steps={t.steps} />
        <p className="mt-4 break-all text-[0.7rem] text-muted">Transfer {t.id}{t.ledger_entry_id && ` · ledger entry ${t.ledger_entry_id}`}</p>
      </div>
      <div className="mt-6 text-center"><Button onClick={onNew}>New transfer</Button></div>
    </Card>
  );
}

export default function TransferPage() {
  const toast = useToast();
  const accounts = useApi<{ accounts: Account[] }>("/v1/accounts");
  const payees = useApi<{ payees: Payee[] }>("/v1/payees");
  const history = useApi<{ transfers: Transfer[] }>("/v1/transfers?page_size=10");
  const accs = (accounts.data?.accounts ?? []).filter((a) => a.status !== "CLOSED");

  const [mode, setMode] = useState<"own" | "payee" | "number">("number");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [amount, setAmount] = useState("");
  const [desc, setDesc] = useState("");
  const [review, setReview] = useState<{ key: string } | null>(null);
  const [result, setResult] = useState<Transfer | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [showPayees, setShowPayees] = useState(false);
  const [detail, setDetail] = useState<Transfer | null>(null);

  useEffect(() => { if (!from && accs[0]) setFrom(accs[0].id); }, [accs, from]);
  const fromAcc = accs.find((a) => a.id === from);
  const look = useLookup(mode === "number" ? to : "");
  const minor = parseNu(amount);
  const toName = useMemo(() => {
    if (mode === "own") return accs.find((a) => a.account_number === to)?.nickname;
    if (mode === "payee") return payees.data?.payees.find((p) => p.account_number === to)?.holder_name;
    return look.holder_name;
  }, [mode, to, accs, payees.data, look.holder_name]);

  const valid = !!fromAcc && /^\d{12}$/.test(to) && to !== fromAcc.account_number && !!minor && !!toName;

  async function confirm() {
    if (!review) return;
    setBusy(true); setErr("");
    try {
      const t = await api("/v1/transfers", {
        body: { from_account_id: from, to_account_number: to, amount: String(minor), description: desc },
        idempotencyKey: review.key,
      });
      setResult(t);
      setReview(null);
      accounts.reload(); history.reload();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  }

  const reset = () => { setResult(null); setAmount(""); setDesc(""); setTo(""); };

  if (accounts.loading) return <Spinner />;
  if (!accs.length) return <><PageHeader title="Transfer money" /><Card><Empty title="No account to send from">Open an account first.</Empty></Card></>;

  return (
    <>
      <PageHeader title="Transfer money" subtitle="Instant transfers to any CBMP Bank account, screened for fraud and posted to the ledger exactly once."
        action={<Button variant="secondary" onClick={() => setShowPayees(true)}><BookUser className="h-4 w-4" />Payees</Button>} />
      <div className="grid gap-6 lg:grid-cols-[1.1fr_1fr]">
        {result ? <ResultView t={result} onNew={reset} /> : (
          <Card title={review ? "Review transfer" : "Transfer details"}>
            {review ? (
              <div className="space-y-4">
                <dl className="divide-y divide-[var(--border)] rounded-xl bg-[var(--surface-2)] px-4 text-sm">
                  {[["From", `${fromAcc?.nickname} · ${fromAcc?.account_number}`], ["To", `${toName} · ${to}`], ["Amount", nu(minor)], ["Reference", desc || "—"]].map(([k, v]) => (
                    <div key={k} className="flex justify-between gap-4 py-3"><dt className="text-muted">{k}</dt><dd className="text-right font-medium">{v}</dd></div>
                  ))}
                </dl>
                {err && <Alert>{err}</Alert>}
                <p className="text-xs text-muted">Request key <span className="font-mono">{review.key.slice(0, 8)}</span>: submitting twice will not send the money twice.</p>
                <div className="flex gap-3">
                  <Button variant="secondary" onClick={() => setReview(null)} disabled={busy}>Back</Button>
                  <Button className="flex-1" onClick={confirm} loading={busy}>Confirm and send {nu(minor)}</Button>
                </div>
              </div>
            ) : (
              <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); setErr(""); setReview({ key: crypto.randomUUID() }); }}>
                <Field label="From account">
                  <select className="input" value={from} onChange={(e) => setFrom(e.target.value)}>
                    {accs.map((a) => <option key={a.id} value={a.id}>{a.nickname} · {a.account_number} · {nu(a.available_balance)} available</option>)}
                  </select>
                </Field>
                <div className="flex gap-1 rounded-xl bg-[var(--surface-2)] p-1 text-sm font-medium">
                  {([["number", "Account number"], ["payee", "Saved payee"], ["own", "My accounts"]] as const).map(([k, l]) => (
                    <button type="button" key={k} onClick={() => { setMode(k); setTo(""); }} className={cx("flex-1 rounded-lg px-3 py-1.5", mode === k ? "surface shadow-sm" : "text-muted")}>{l}</button>
                  ))}
                </div>
                {mode === "number" && (
                  <Field label="Beneficiary account number" error={look.error} hint={look.loading ? "Looking up…" : look.holder_name ? `✓ ${look.holder_name}` : "12 digits"}>
                    <input className="input font-mono" inputMode="numeric" maxLength={12} value={to} onChange={(e) => setTo(e.target.value.replace(/\D/g, ""))} />
                  </Field>
                )}
                {mode === "payee" && (
                  <Field label="Payee">
                    <select className="input" value={to} onChange={(e) => setTo(e.target.value)}>
                      <option value="">Choose a payee…</option>
                      {payees.data?.payees.map((p) => <option key={p.id} value={p.account_number}>{p.nickname} · {p.account_number}</option>)}
                    </select>
                  </Field>
                )}
                {mode === "own" && (
                  <Field label="To account">
                    <select className="input" value={to} onChange={(e) => setTo(e.target.value)}>
                      <option value="">Choose an account…</option>
                      {accs.filter((a) => a.id !== from).map((a) => <option key={a.id} value={a.account_number}>{a.nickname} · {a.account_number}</option>)}
                    </select>
                  </Field>
                )}
                <Field label="Amount (Nu.)" hint={fromAcc && `Available ${nu(fromAcc.available_balance)} · daily limit ${nu(fromAcc.daily_transfer_limit)}`}
                  error={minor && fromAcc && minor > n(fromAcc.available_balance) ? "More than your available balance" : undefined}>
                  <input className="input text-lg num" inputMode="decimal" placeholder="0.00" value={amount} onChange={(e) => setAmount(e.target.value)} />
                </Field>
                <Field label="Reference (optional)"><input className="input" maxLength={140} value={desc} onChange={(e) => setDesc(e.target.value)} placeholder="e.g. Rent October" /></Field>
                <Button className="w-full" disabled={!valid}>Review <ArrowRight className="h-4 w-4" /></Button>
              </form>
            )}
          </Card>
        )}
        <Card title="Recent transfers">
          {history.loading ? <Spinner /> : !history.data?.transfers?.length ? <Empty title="No transfers yet" /> : (
            <Table head={["To", "Amount", "Status"]}>
              {history.data.transfers.map((t) => (
                <tr key={t.id} className="cursor-pointer hover:bg-[var(--surface-2)]" onClick={() => setDetail(t)}>
                  <td className="px-5 py-3"><p className="font-medium">{t.to_name}</p><p className="text-xs text-muted">{date(t.created_at)}</p></td>
                  <td className="px-5 py-3"><Money value={t.amount} /></td>
                  <td className="px-5 py-3"><Badge>{t.status}</Badge></td>
                </tr>
              ))}
            </Table>
          )}
        </Card>
      </div>
      <PayeesModal open={showPayees} onClose={() => setShowPayees(false)} payees={payees.data?.payees ?? []} reload={payees.reload} />
      <Modal open={!!detail} onClose={() => setDetail(null)} title="Transfer details">
        {detail && (
          <div className="space-y-4">
            <div className="flex items-center justify-between"><Money value={detail.amount} className="text-2xl font-semibold" /><Badge>{detail.status}</Badge></div>
            <p className="text-sm text-muted">{detail.from_account_number} → {detail.to_account_number} ({detail.to_name})</p>
            {detail.failure_reason && <Alert>{detail.failure_reason}</Alert>}
            <p className="text-sm">Fraud decision: <Badge>{detail.fraud_decision || "—"}</Badge> <span className="text-muted">score {detail.fraud_score}</span></p>
            <SagaTimeline steps={detail.steps} />
          </div>
        )}
      </Modal>
    </>
  );
}
