"use client";

import { useState } from "react";
import { Alert, Badge, Button, Card, cx, Empty, Field, Modal, Money, PageHeader, Spinner, Stat, Table, useToast } from "@/components/ui";
import { useMe } from "@/components/me";
import { api, errMsg, useApi } from "@/lib/api";
import { date, hasRole, nu, parseNu } from "@/lib/format";

const tabs = ["OPEN", "APPROVED", "REJECTED", "CLOSED"];

function Rules({ canEdit }: { canEdit: boolean }) {
  const toast = useToast();
  const { data, reload } = useApi<{ rules: any[] }>("/v1/staff/fraud/rules");
  const [edit, setEdit] = useState<any>(null);
  const isCount = (code: string) => code === "VELOCITY";
  return (
    <Card title="Screening rules" action={<span className="text-xs text-muted">Changes apply to the next transaction</span>}>
      <Table head={["Rule", "Threshold", "Action", "Weight", "Enabled", ""]}>
        {data?.rules.map((r) => (
          <tr key={r.id}>
            <td className="px-5 py-3"><p className="font-mono text-xs font-semibold">{r.code}</p><p className="text-xs text-muted">{r.description}</p></td>
            <td className="px-5 py-3 num">{isCount(r.code) ? `${r.threshold} in ${r.window_minutes} min` : nu(r.threshold)}</td>
            <td className="px-5 py-3"><Badge>{r.action}</Badge></td>
            <td className="px-5 py-3">{r.weight}</td>
            <td className="px-5 py-3">{r.enabled ? <Badge tone="green">on</Badge> : <Badge tone="gray">off</Badge>}</td>
            <td className="px-5 py-3 text-right">{canEdit && <Button size="sm" variant="secondary" onClick={() => setEdit({ ...r, thresholdText: isCount(r.code) ? String(r.threshold) : String(Number(r.threshold) / 100) })}>Edit</Button>}</td>
          </tr>
        ))}
      </Table>
      <Modal open={!!edit} onClose={() => setEdit(null)} title={`Edit ${edit?.code ?? ""}`}>
        {edit && (
          <form className="space-y-4" onSubmit={async (e) => {
            e.preventDefault();
            const threshold = isCount(edit.code) ? Number(edit.thresholdText) : parseNu(edit.thresholdText);
            try {
              await api(`/v1/staff/fraud/rules/${edit.id}`, { method: "PATCH", body: { threshold: String(threshold), action: edit.action, enabled: edit.enabled, window_minutes: edit.window_minutes } });
              toast("Rule updated"); setEdit(null); reload();
            } catch (e) { toast(errMsg(e), "red"); }
          }}>
            <Field label={isCount(edit.code) ? "Max movements in window" : "Threshold (Nu.)"}><input className="input num" value={edit.thresholdText} onChange={(e) => setEdit({ ...edit, thresholdText: e.target.value })} /></Field>
            {isCount(edit.code) && <Field label="Window (minutes)"><input className="input" type="number" min={1} value={edit.window_minutes} onChange={(e) => setEdit({ ...edit, window_minutes: Number(e.target.value) })} /></Field>}
            <Field label="Action">
              <select className="input" value={edit.action} onChange={(e) => setEdit({ ...edit, action: e.target.value })}><option>REVIEW</option><option>BLOCK</option></select>
            </Field>
            <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={edit.enabled} onChange={(e) => setEdit({ ...edit, enabled: e.target.checked })} />Enabled</label>
            <Button className="w-full">Save rule</Button>
          </form>
        )}
      </Modal>
    </Card>
  );
}

export default function FraudPage() {
  const { me } = useMe();
  const toast = useToast();
  const [tab, setTab] = useState("OPEN");
  const stats = useApi<any>("/v1/staff/fraud/stats");
  const alerts = useApi<{ alerts: any[] }>(`/v1/staff/fraud/alerts?status=${tab}&page_size=50`, [tab]);
  const [sel, setSel] = useState<any>(null);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const analyst = hasRole(me?.roles, "fraud_analyst");

  async function decide(approve: boolean) {
    setBusy(true);
    try {
      await api(`/v1/staff/fraud/alerts/${sel.id}/decide`, { body: { approve, note } });
      toast(approve ? "Released: the transaction continues" : "Rejected: funds returned to the customer");
      setSel(null); alerts.reload(); stats.reload();
    } catch (e) { toast(errMsg(e), "red"); } finally { setBusy(false); }
  }

  return (
    <>
      <PageHeader title="Fraud & risk" subtitle="Every money movement is screened before it is posted. Review held transactions here." />
      <div className="grid gap-4 sm:grid-cols-5">
        <Stat label="Screened" value={Number(stats.data?.screened ?? 0).toLocaleString()} />
        <Stat label="Allowed" value={Number(stats.data?.allowed ?? 0).toLocaleString()} />
        <Stat label="Sent to review" value={Number(stats.data?.reviewed ?? 0).toLocaleString()} />
        <Stat label="Blocked" value={Number(stats.data?.blocked ?? 0).toLocaleString()} />
        <Stat label="Open alerts" value={Number(stats.data?.open_alerts ?? 0).toLocaleString()} />
      </div>
      <div className="mb-4 mt-6 flex gap-1 rounded-xl bg-[var(--surface-2)] p-1 text-sm font-medium sm:w-fit">
        {tabs.map((t) => <button key={t} onClick={() => setTab(t)} className={cx("flex-1 rounded-lg px-4 py-1.5", tab === t ? "surface shadow-sm" : "text-muted")}>{t.toLowerCase()}</button>)}
      </div>
      <Card>
        {alerts.loading ? <Spinner /> : !alerts.data?.alerts?.length ? <Empty title={`No ${tab.toLowerCase()} alerts`} /> : (
          <Table head={["When", "Channel", "Amount", "Decision", "Score", "Reasons", ""]}>
            {alerts.data.alerts.map((a) => (
              <tr key={a.id} className="hover:bg-[var(--surface-2)]">
                <td className="px-5 py-3 text-muted">{date(a.created_at)}</td>
                <td className="px-5 py-3">{a.channel}</td>
                <td className="px-5 py-3 font-medium"><Money value={a.amount} /></td>
                <td className="px-5 py-3"><Badge>{a.decision}</Badge></td>
                <td className="px-5 py-3">{a.score}</td>
                <td className="max-w-xs px-5 py-3 text-xs text-muted">{a.reasons.map((r: string) => r.split(":")[0]).join(", ")}</td>
                <td className="px-5 py-3 text-right"><Button size="sm" variant="secondary" onClick={() => { setSel(a); setNote(""); }}>{a.status === "OPEN" && analyst ? "Decide" : "View"}</Button></td>
              </tr>
            ))}
          </Table>
        )}
      </Card>
      <div className="mt-6"><Rules canEdit={analyst} /></div>
      <Modal open={!!sel} onClose={() => setSel(null)} title="Alert">
        {sel && (
          <div className="space-y-4">
            <div className="flex items-center justify-between"><Money value={sel.amount} className="text-2xl font-semibold" /><Badge>{sel.status}</Badge></div>
            <dl className="grid grid-cols-2 gap-3 text-sm">
              {[["Channel", sel.channel], ["Reference", `${sel.reference_type}`], ["Counterparty", sel.counterparty], ["Score", `${sel.score} / 100`]].map(([k, v]) => (
                <div key={k}><dt className="text-xs text-muted">{k}</dt><dd className="break-all font-medium">{v}</dd></div>
              ))}
            </dl>
            <ul className="space-y-1 rounded-xl bg-[var(--surface-2)] p-3 text-sm">{sel.reasons.map((r: string) => <li key={r}>• {r}</li>)}</ul>
            {sel.analyst_note && <Alert tone="blue">Analyst note: {sel.analyst_note}</Alert>}
            {sel.status === "OPEN" && analyst && (
              <>
                <Field label="Note"><textarea className="input" rows={2} value={note} onChange={(e) => setNote(e.target.value)} placeholder="e.g. Customer confirmed by phone" /></Field>
                <div className="flex gap-3">
                  <Button variant="danger" className="flex-1" loading={busy} onClick={() => decide(false)}>Reject transaction</Button>
                  <Button variant="success" className="flex-1" loading={busy} onClick={() => decide(true)}>Release</Button>
                </div>
              </>
            )}
          </div>
        )}
      </Modal>
    </>
  );
}
