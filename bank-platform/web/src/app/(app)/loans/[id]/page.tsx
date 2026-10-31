"use client";

import Link from "next/link";
import { use, useState } from "react";
import { ArrowLeft } from "lucide-react";
import { Alert, Badge, Button, Card, Field, Money, PageHeader, Spinner, Stat, Table, useToast } from "@/components/ui";
import { api, errMsg, useApi } from "@/lib/api";
import { date, nu } from "@/lib/format";
import type { Account } from "@/lib/types";

export default function LoanPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  const toast = useToast();
  const { data: l, loading, error, reload } = useApi<any>(`/v1/loans/${id}`);
  const accounts = useApi<{ accounts: Account[] }>("/v1/accounts");
  const [from, setFrom] = useState("");
  const [busy, setBusy] = useState(false);
  if (loading) return <Spinner />;
  if (error || !l) return <Alert>{error?.message ?? "Loan not found"}</Alert>;
  const accs = (accounts.data?.accounts ?? []).filter((a) => a.status === "ACTIVE");
  const src = from || l.disbursement_account_id;

  async function repay() {
    setBusy(true);
    try {
      await api(`/v1/loans/${id}/repay`, { body: { from_account_id: src } });
      toast("EMI paid");
      reload(); accounts.reload();
    } catch (e) { toast(errMsg(e), "red"); } finally { setBusy(false); }
  }

  return (
    <>
      <Link href="/loans" className="mb-4 inline-flex items-center gap-1 text-sm text-muted hover:text-[var(--text)]"><ArrowLeft className="h-4 w-4" />Loans</Link>
      <PageHeader title={l.product_name} subtitle={`${(l.annual_rate_bps / 100).toFixed(2)}% p.a. · ${l.term_months} months · applied ${date(l.created_at, false)}`} action={<Badge>{l.status}</Badge>} />
      {l.status === "APPLIED" && <div className="mb-4"><Alert tone="amber">Waiting for a loan officer's decision.</Alert></div>}
      {l.status === "REJECTED" && <div className="mb-4"><Alert>Not approved: {l.reviewer_note}</Alert></div>}
      <div className="grid gap-4 sm:grid-cols-4">
        <Stat label="Principal" value={nu(l.principal)} />
        <Stat label="Monthly EMI" value={nu(l.emi)} />
        <Stat label="Outstanding" value={nu(l.outstanding_principal)} hint={`${l.paid_instalments} of ${l.term_months} paid`} />
        <Stat label="Next due" value={l.next_due_date ? nu(l.next_due_amount) : "—"} hint={l.next_due_date ? date(l.next_due_date, false) : undefined} />
      </div>
      {l.status === "ACTIVE" && (
        <Card className="mt-6" title="Pay next instalment">
          <div className="flex flex-wrap items-end gap-3">
            <div className="min-w-64 flex-1">
              <Field label="Pay from">
                <select className="input" value={src} onChange={(e) => setFrom(e.target.value)}>
                  {accs.map((a) => <option key={a.id} value={a.id}>{a.nickname} · {a.account_number} · {nu(a.available_balance)}</option>)}
                </select>
              </Field>
            </div>
            <Button onClick={repay} loading={busy}>Pay {nu(l.next_due_amount)}</Button>
          </div>
          <p className="mt-2 text-xs text-muted">Each instalment can only be paid once, even if you click twice.</p>
        </Card>
      )}
      {l.schedule?.length > 0 && (
        <Card className="mt-6" title="Repayment schedule">
          <Table head={["#", "Due", "Principal", "Interest", "EMI", "Balance after", "Status"]}>
            {l.schedule.map((s: any) => (
              <tr key={s.number}>
                <td className="px-5 py-2.5 text-muted">{s.number}</td>
                <td className="px-5 py-2.5">{date(s.due_date, false)}</td>
                <td className="px-5 py-2.5"><Money value={s.principal} /></td>
                <td className="px-5 py-2.5"><Money value={s.interest} /></td>
                <td className="px-5 py-2.5 font-medium"><Money value={s.amount} /></td>
                <td className="px-5 py-2.5"><Money value={s.balance_after} /></td>
                <td className="px-5 py-2.5"><Badge>{s.status}</Badge></td>
              </tr>
            ))}
          </Table>
        </Card>
      )}
    </>
  );
}
