"use client";

import { useEffect, useState } from "react";
import { BookOpen, GraduationCap, Landmark, Shield, Smartphone, Wifi, Zap, Droplets } from "lucide-react";
import { Alert, Badge, Button, Card, cx, Empty, Field, Modal, Money, PageHeader, Spinner, Table, useToast } from "@/components/ui";
import { SagaTimeline } from "@/components/saga";
import { api, errMsg, useApi } from "@/lib/api";
import { date, nu, parseNu } from "@/lib/format";
import type { Account } from "@/lib/types";

type Biller = { id: string; name: string; category: string; reference_label: string; reference_hint: string };
const icons: Record<string, any> = { electricity: Zap, water: Droplets, mobile: Smartphone, broadband: Wifi, "property-tax": Landmark, tuition: GraduationCap, insurance: Shield };

export default function PaymentsPage() {
  const toast = useToast();
  const billers = useApi<{ billers: Biller[] }>("/v1/billers");
  const accounts = useApi<{ accounts: Account[] }>("/v1/accounts");
  const payments = useApi<{ payments: any[] }>("/v1/payments?page_size=15");
  const accs = (accounts.data?.accounts ?? []).filter((a) => a.status === "ACTIVE");
  const [biller, setBiller] = useState<Biller | null>(null);
  const [from, setFrom] = useState("");
  const [ref, setRef] = useState("");
  const [amount, setAmount] = useState("");
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [detail, setDetail] = useState<any>(null);

  useEffect(() => { if (!from && accs[0]) setFrom(accs[0].id); }, [accs, from]);
  const minor = parseNu(amount);

  const open = (b: Biller) => { setBiller(b); setRef(""); setAmount(""); setErr(""); setKey(crypto.randomUUID()); };

  async function pay(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true); setErr("");
    try {
      const p = await api("/v1/payments", { body: { from_account_id: from, biller_id: biller!.id, customer_reference: ref, amount: String(minor) }, idempotencyKey: key });
      setBiller(null);
      setDetail(p);
      toast(p.status === "SUCCESS" ? `Paid ${nu(p.amount)} to ${p.biller_name}` : `Payment ${p.status.toLowerCase().replace("_", " ")}`, p.status === "SUCCESS" ? "green" : "red");
      payments.reload(); accounts.reload();
    } catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
  }

  return (
    <>
      <PageHeader title="Pay bills" subtitle="Payments go to billers through the external payment gateway (simulated)." />
      <Card title="Choose a biller">
        {billers.loading ? <Spinner /> : (
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
            {billers.data?.billers.map((b) => {
              const Icon = icons[b.id] ?? BookOpen;
              return (
                <button key={b.id} onClick={() => open(b)} disabled={!accs.length}
                  className="rounded-2xl border border-base p-4 text-left transition-colors hover:border-brand-200 hover:bg-brand-50/40 disabled:opacity-50 dark:hover:bg-brand-500/5">
                  <Icon className="h-5 w-5 text-brand-600" />
                  <p className="mt-3 text-sm font-medium leading-tight">{b.name}</p>
                  <p className="text-xs text-muted">{b.category}</p>
                </button>
              );
            })}
          </div>
        )}
        {!accounts.loading && !accs.length && <div className="mt-4"><Alert tone="amber">Open an active account to pay bills.</Alert></div>}
      </Card>

      <Card title="Payment history" className="mt-6">
        {payments.loading ? <Spinner /> : !payments.data?.payments?.length ? <Empty title="No payments yet" /> : (
          <Table head={["Biller", "Reference", "Amount", "Status", "Date"]}>
            {payments.data.payments.map((p) => (
              <tr key={p.id} className="cursor-pointer hover:bg-[var(--surface-2)]" onClick={() => setDetail(p)}>
                <td className="px-5 py-3 font-medium">{p.biller_name}</td>
                <td className="px-5 py-3 font-mono text-xs">{p.customer_reference}</td>
                <td className="px-5 py-3"><Money value={p.amount} /></td>
                <td className="px-5 py-3"><Badge>{p.status}</Badge></td>
                <td className="px-5 py-3 text-muted">{date(p.created_at)}</td>
              </tr>
            ))}
          </Table>
        )}
      </Card>

      <Modal open={!!biller} onClose={() => setBiller(null)} title={biller?.name ?? ""}>
        <form className="space-y-4" onSubmit={pay}>
          {err && <Alert>{err}</Alert>}
          <Field label="Pay from">
            <select className="input" value={from} onChange={(e) => setFrom(e.target.value)}>
              {accs.map((a) => <option key={a.id} value={a.id}>{a.nickname} · {a.account_number} · {nu(a.available_balance)}</option>)}
            </select>
          </Field>
          <Field label={biller?.reference_label ?? "Reference"} hint={<>{biller?.reference_hint}. Tip: a reference containing <span className="font-mono">FAIL</span> simulates a biller decline.</>}>
            <input className="input font-mono" required value={ref} onChange={(e) => setRef(e.target.value)} />
          </Field>
          <Field label="Amount (Nu.)"><input className="input text-lg num" inputMode="decimal" placeholder="0.00" value={amount} onChange={(e) => setAmount(e.target.value)} /></Field>
          <Button className="w-full" loading={busy} disabled={!minor || !ref}>Pay {minor ? nu(minor) : ""}</Button>
        </form>
      </Modal>

      <Modal open={!!detail} onClose={() => setDetail(null)} title="Payment details">
        {detail && (
          <div className="space-y-4">
            <div className="flex items-center justify-between"><Money value={detail.amount} className="text-2xl font-semibold" /><Badge>{detail.status}</Badge></div>
            <p className="text-sm text-muted">{detail.biller_name} · ref <span className="font-mono">{detail.customer_reference}</span></p>
            {detail.gateway_reference && <p className="text-sm">Gateway confirmation <span className="font-mono">{detail.gateway_reference}</span></p>}
            {detail.failure_reason && <Alert>{detail.failure_reason}</Alert>}
            <div className={cx(detail.status === "PENDING_REVIEW" && "mb-2")}>
              {detail.status === "PENDING_REVIEW" && <Alert tone="amber">Held for a security review; funds are reserved.</Alert>}
            </div>
            <SagaTimeline steps={detail.steps} />
          </div>
        )}
      </Modal>
    </>
  );
}
