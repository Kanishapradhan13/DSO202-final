"use client";

import { useState } from "react";
import { Alert, Badge, Button, Card, cx, Empty, Field, Modal, Money, PageHeader, Spinner, Table, useToast } from "@/components/ui";
import { useMe } from "@/components/me";
import { api, errMsg, useApi } from "@/lib/api";
import { date, hasRole, n, nu } from "@/lib/format";

const tabs = ["APPLIED", "ACTIVE", "REJECTED", "CLOSED"];

export default function LoanDesk() {
  const { me } = useMe();
  const toast = useToast();
  const [tab, setTab] = useState("APPLIED");
  const list = useApi<{ loans: any[] }>(`/v1/staff/loans?status=${tab}&page_size=50`, [tab]);
  const [sel, setSel] = useState<any>(null);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const canReview = hasRole(me?.roles, "loan_officer");

  async function decide(approve: boolean) {
    setBusy(true);
    try {
      await api(`/v1/staff/loans/${sel.id}/review`, { body: { approve, note } });
      toast(approve ? `Approved and disbursed ${nu(sel.principal)}` : "Application rejected");
      setSel(null); list.reload();
    } catch (e) { toast(errMsg(e), "red"); } finally { setBusy(false); }
  }

  const dti = (l: any) => (n(l.monthly_income) ? (n(l.emi) / n(l.monthly_income)) * 100 : 0);

  return (
    <>
      <PageHeader title="Loan desk" subtitle="Approving a loan disburses it immediately through the ledger." />
      <div className="mb-4 flex gap-1 rounded-xl bg-[var(--surface-2)] p-1 text-sm font-medium sm:w-fit">
        {tabs.map((t) => <button key={t} onClick={() => setTab(t)} className={cx("flex-1 rounded-lg px-4 py-1.5", tab === t ? "surface shadow-sm" : "text-muted")}>{t.toLowerCase()}</button>)}
      </div>
      <Card>
        {list.loading ? <Spinner /> : !list.data?.loans?.length ? <Empty title={`No ${tab.toLowerCase()} loans`} /> : (
          <Table head={["Applicant", "Product", "Amount", "Term", "EMI", "EMI / income", "Applied", ""]}>
            {list.data.loans.map((l) => (
              <tr key={l.id} className="hover:bg-[var(--surface-2)]">
                <td className="px-5 py-3 font-medium">{l.applicant_name}</td>
                <td className="px-5 py-3">{l.product_name}</td>
                <td className="px-5 py-3"><Money value={l.principal} /></td>
                <td className="px-5 py-3">{l.term_months} mo</td>
                <td className="px-5 py-3"><Money value={l.emi} /></td>
                <td className="px-5 py-3"><Badge tone={dti(l) > 50 ? "red" : dti(l) > 35 ? "amber" : "green"}>{`${dti(l).toFixed(0)}%`}</Badge></td>
                <td className="px-5 py-3 text-muted">{date(l.created_at, false)}</td>
                <td className="px-5 py-3 text-right"><Button size="sm" variant="secondary" onClick={() => { setSel(l); setNote(""); }}>{l.status === "APPLIED" && canReview ? "Review" : "View"}</Button></td>
              </tr>
            ))}
          </Table>
        )}
      </Card>
      <Modal open={!!sel} onClose={() => setSel(null)} title={`${sel?.product_name ?? ""} · ${sel?.applicant_name ?? ""}`}>
        {sel && (
          <div className="space-y-4">
            <dl className="grid grid-cols-2 gap-3 text-sm">
              {[["Principal", nu(sel.principal)], ["Rate", `${(sel.annual_rate_bps / 100).toFixed(2)}% p.a.`], ["Term", `${sel.term_months} months`], ["EMI", nu(sel.emi)],
                ["Total interest", nu(sel.total_interest)], ["Monthly income", nu(sel.monthly_income)], ["Disburse to", sel.disbursement_account_number], ["Purpose", sel.purpose || "—"]].map(([k, v]) => (
                <div key={k}><dt className="text-xs text-muted">{k}</dt><dd className="font-medium">{v}</dd></div>
              ))}
            </dl>
            {dti(sel) > 50 && <Alert tone="amber">EMI is {dti(sel).toFixed(0)}% of declared income, above the 50% affordability guideline.</Alert>}
            {sel.reviewer_note && <Alert tone="blue">Reviewer note: {sel.reviewer_note}</Alert>}
            {sel.status === "APPLIED" && canReview && (
              <>
                <Field label="Decision note (required to reject)"><textarea className="input" rows={2} value={note} onChange={(e) => setNote(e.target.value)} /></Field>
                <div className="flex gap-3">
                  <Button variant="danger" className="flex-1" loading={busy} disabled={!note.trim()} onClick={() => decide(false)}>Reject</Button>
                  <Button variant="success" className="flex-1" loading={busy} onClick={() => decide(true)}>Approve & disburse</Button>
                </div>
              </>
            )}
          </div>
        )}
      </Modal>
    </>
  );
}
