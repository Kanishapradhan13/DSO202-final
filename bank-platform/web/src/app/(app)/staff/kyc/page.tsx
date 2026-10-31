"use client";

import { useState } from "react";
import { CheckCircle2, AlertTriangle, XCircle, Info } from "lucide-react";
import { Alert, Badge, Button, Card, cx, Empty, Field, Modal, PageHeader, Spinner, Table, useToast } from "@/components/ui";
import { useMe } from "@/components/me";
import { api, errMsg, useApi } from "@/lib/api";
import { date, hasRole } from "@/lib/format";

const tabs = ["PENDING", "APPROVED", "REJECTED"];

function CheckLine({ c }: { c: string }) {
  const Icon = c.startsWith("PASS") ? CheckCircle2 : c.startsWith("FAIL") ? XCircle : c.startsWith("WARN") ? AlertTriangle : Info;
  const color = c.startsWith("PASS") ? "text-emerald-600" : c.startsWith("FAIL") ? "text-red-600" : c.startsWith("WARN") ? "text-amber-600" : "text-muted";
  return <li className="flex items-start gap-2 text-sm"><Icon className={cx("mt-0.5 h-4 w-4 shrink-0", color)} />{c.replace(/^\w+: /, "")}</li>;
}

export default function KycQueue() {
  const { me } = useMe();
  const toast = useToast();
  const [tab, setTab] = useState("PENDING");
  const list = useApi<{ cases: any[] }>(`/v1/staff/kyc?status=${tab}&page_size=50`, [tab]);
  const [sel, setSel] = useState<any>(null);
  const [note, setNote] = useState("");
  const [tier, setTier] = useState("");
  const [busy, setBusy] = useState(false);
  const canReview = hasRole(me?.roles, "ops_officer");

  async function openCase(id: string) {
    try { const c = await api(`/v1/staff/kyc/${id}`); setSel(c); setNote(""); setTier(c.risk_tier); } catch (e) { toast(errMsg(e), "red"); }
  }
  async function decide(approve: boolean) {
    setBusy(true);
    try {
      await api(`/v1/staff/kyc/${sel.id}/review`, { body: { approve, note, risk_tier: tier } });
      toast(approve ? `KYC approved for ${sel.full_name}` : "KYC rejected");
      setSel(null); list.reload();
    } catch (e) { toast(errMsg(e), "red"); } finally { setBusy(false); }
  }

  return (
    <>
      <PageHeader title="KYC reviews" subtitle="Verify customer identity before their accounts can transact." />
      <div className="mb-4 flex gap-1 rounded-xl bg-[var(--surface-2)] p-1 text-sm font-medium sm:w-fit">
        {tabs.map((t) => <button key={t} onClick={() => setTab(t)} className={cx("flex-1 rounded-lg px-4 py-1.5", tab === t ? "surface shadow-sm" : "text-muted")}>{t.toLowerCase()}</button>)}
      </div>
      <Card>
        {list.loading ? <Spinner /> : !list.data?.cases?.length ? <Empty title={`No ${tab.toLowerCase()} cases`} /> : (
          <Table head={["Customer", "Document", "Risk", "Submitted", "Status", ""]}>
            {list.data.cases.map((c) => (
              <tr key={c.id} className="hover:bg-[var(--surface-2)]">
                <td className="px-5 py-3 font-medium">{c.full_name}</td>
                <td className="px-5 py-3"><span className="text-muted">{c.document_type}</span> <span className="font-mono">{c.document_number}</span></td>
                <td className="px-5 py-3"><Badge tone={c.risk_tier === "HIGH" ? "red" : c.risk_tier === "MEDIUM" ? "amber" : "green"}>{c.risk_tier}</Badge></td>
                <td className="px-5 py-3 text-muted">{date(c.submitted_at)}</td>
                <td className="px-5 py-3"><Badge>{c.status}</Badge></td>
                <td className="px-5 py-3 text-right"><Button size="sm" variant="secondary" onClick={() => openCase(c.id)}>{c.status === "PENDING" && canReview ? "Review" : "View"}</Button></td>
              </tr>
            ))}
          </Table>
        )}
      </Card>

      <Modal open={!!sel} onClose={() => setSel(null)} title={`KYC · ${sel?.full_name ?? ""}`} wide>
        {sel && (
          <div className="grid gap-6 md:grid-cols-2">
            <div className="space-y-4">
              <dl className="space-y-2 text-sm">
                {[["Document", `${sel.document_type} · ${sel.document_number}`], ["Date of birth", sel.date_of_birth], ["Submitted", date(sel.submitted_at)], ["Status", sel.status]].map(([k, v]) => (
                  <div key={k} className="flex justify-between gap-4"><dt className="text-muted">{k}</dt><dd className="text-right font-medium">{v}</dd></div>
                ))}
              </dl>
              <div>
                <p className="mb-2 text-xs font-semibold uppercase tracking-wide text-muted">Automated checks</p>
                <ul className="space-y-1.5">{sel.auto_checks?.map((c: string) => <CheckLine key={c} c={c} />)}</ul>
              </div>
              {sel.review_note && <Alert tone="blue">Reviewer note: {sel.review_note}</Alert>}
            </div>
            <div className="space-y-4">
              {sel.has_document ? (
                // eslint-disable-next-line @next/next/no-img-element
                <img src={`data:${sel.document_mime || "image/jpeg"};base64,${sel.document_image}`} alt="Identity document" className="max-h-64 w-full rounded-xl border border-base object-contain" />
              ) : <div className="flex h-40 items-center justify-center rounded-xl border border-dashed border-base text-sm text-muted">No document image</div>}
              {sel.status === "PENDING" && canReview && (
                <>
                  <Field label="Risk tier">
                    <select className="input" value={tier} onChange={(e) => setTier(e.target.value)}>{["LOW", "MEDIUM", "HIGH"].map((t) => <option key={t}>{t}</option>)}</select>
                  </Field>
                  <Field label="Note (required to reject)"><textarea className="input" rows={2} value={note} onChange={(e) => setNote(e.target.value)} /></Field>
                  <div className="flex gap-3">
                    <Button variant="danger" className="flex-1" loading={busy} disabled={!note.trim()} onClick={() => decide(false)}>Reject</Button>
                    <Button variant="success" className="flex-1" loading={busy} onClick={() => decide(true)}>Approve</Button>
                  </div>
                </>
              )}
            </div>
          </div>
        )}
      </Modal>
    </>
  );
}
