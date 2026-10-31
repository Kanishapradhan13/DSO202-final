"use client";

import Link from "next/link";
import { useState } from "react";
import { CheckCircle2, Clock, FileUp, IdCard, UserRound, Wallet } from "lucide-react";
import { Alert, Badge, Button, Card, cx, Field, PageHeader, Spinner, useToast } from "@/components/ui";
import { api, errMsg, useApi } from "@/lib/api";
import { useMe } from "@/components/me";
import { date } from "@/lib/format";

function Step({ n, title, icon: Icon, state, children }: { n: number; title: string; icon: any; state: "done" | "current" | "locked"; children?: React.ReactNode }) {
  return (
    <Card className={cx(state === "locked" && "opacity-60")}>
      <div className="flex items-start gap-4">
        <div className={cx("flex h-10 w-10 shrink-0 items-center justify-center rounded-full",
          state === "done" ? "bg-emerald-600 text-white" : state === "current" ? "bg-brand-700 text-white" : "bg-[var(--surface-2)] text-muted")}>
          {state === "done" ? <CheckCircle2 className="h-5 w-5" /> : <Icon className="h-5 w-5" />}
        </div>
        <div className="min-w-0 flex-1">
          <p className="text-xs font-semibold uppercase tracking-wide text-muted">Step {n}</p>
          <h2 className="text-lg font-semibold">{title}</h2>
          {state !== "locked" && children && <div className="mt-4">{children}</div>}
        </div>
      </div>
    </Card>
  );
}

function ProfileForm({ onDone }: { onDone: () => void }) {
  const { me } = useMe();
  const [f, setF] = useState({ full_name: me?.full_name ?? "", phone: me?.phone ?? "", date_of_birth: "", address: "", city: "Thimphu", occupation: "" });
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const set = (k: keyof typeof f) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => setF({ ...f, [k]: e.target.value });
  return (
    <form className="grid gap-4 sm:grid-cols-2" onSubmit={async (e) => {
      e.preventDefault(); setErr(""); setBusy(true);
      try { await api("/v1/customers/me", { body: f }); onDone(); } catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
    }}>
      {err && <div className="sm:col-span-2"><Alert>{err}</Alert></div>}
      <Field label="Full name"><input className="input" required value={f.full_name} onChange={set("full_name")} /></Field>
      <Field label="Date of birth"><input className="input" type="date" required value={f.date_of_birth} onChange={set("date_of_birth")} /></Field>
      <Field label="Mobile number"><input className="input" value={f.phone} onChange={set("phone")} /></Field>
      <Field label="Occupation"><input className="input" value={f.occupation} onChange={set("occupation")} placeholder="e.g. Student, Civil servant" /></Field>
      <Field label="Address"><input className="input" required value={f.address} onChange={set("address")} /></Field>
      <Field label="Dzongkhag / city">
        <select className="input" value={f.city} onChange={set("city")}>
          {["Thimphu", "Paro", "Punakha", "Wangdue Phodrang", "Chukha", "Phuentsholing", "Bumthang", "Trongsa", "Mongar", "Trashigang", "Samdrup Jongkhar", "Sarpang", "Gelephu", "Haa", "Samtse", "Tsirang", "Dagana", "Zhemgang", "Lhuentse", "Trashiyangtse", "Pemagatshel", "Gasa"].map((c) => <option key={c}>{c}</option>)}
        </select>
      </Field>
      <div className="sm:col-span-2"><Button loading={busy}>Save profile</Button></div>
    </form>
  );
}

function KycForm({ dob, onDone }: { dob?: string; onDone: () => void }) {
  const [f, setF] = useState({ document_type: "CID", document_number: "", date_of_birth: dob ?? "" });
  const [file, setFile] = useState<{ b64: string; mime: string; name: string } | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const pick = (e: React.ChangeEvent<HTMLInputElement>) => {
    const fl = e.target.files?.[0];
    if (!fl) return;
    if (fl.size > 2 * 1024 * 1024) return setErr("Image must be under 2 MB");
    const r = new FileReader();
    r.onload = () => setFile({ b64: String(r.result).split(",")[1], mime: fl.type, name: fl.name });
    r.readAsDataURL(fl);
  };
  return (
    <form className="grid gap-4 sm:grid-cols-2" onSubmit={async (e) => {
      e.preventDefault(); setErr(""); setBusy(true);
      try {
        await api("/v1/kyc", { body: { ...f, document_image: file?.b64, document_mime: file?.mime } });
        onDone();
      } catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
    }}>
      {err && <div className="sm:col-span-2"><Alert>{err}</Alert></div>}
      <Field label="Document type">
        <select className="input" value={f.document_type} onChange={(e) => setF({ ...f, document_type: e.target.value })}>
          <option value="CID">Citizenship ID (CID)</option>
          <option value="PASSPORT">Passport</option>
          <option value="WORK_PERMIT">Work permit</option>
        </select>
      </Field>
      <Field label="Document number" hint={f.document_type === "CID" ? "11 digits" : "6–9 letters or digits"}>
        <input className="input font-mono" required value={f.document_number} onChange={(e) => setF({ ...f, document_number: e.target.value })} />
      </Field>
      <Field label="Date of birth on document"><input className="input" type="date" required value={f.date_of_birth} onChange={(e) => setF({ ...f, date_of_birth: e.target.value })} /></Field>
      <Field label="Photo of document (optional)" hint="JPG or PNG, max 2 MB">
        <label className="input flex cursor-pointer items-center gap-2 text-muted">
          <FileUp className="h-4 w-4" /> <span className="truncate">{file?.name ?? "Choose file…"}</span>
          <input type="file" accept="image/*" className="hidden" onChange={pick} />
        </label>
      </Field>
      <div className="sm:col-span-2"><Button loading={busy}>Submit for verification</Button></div>
    </form>
  );
}

function OpenAccount({ onDone }: { onDone: () => void }) {
  const toast = useToast();
  const [type, setType] = useState("SAVINGS");
  const [nickname, setNickname] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  return (
    <form className="space-y-4" onSubmit={async (e) => {
      e.preventDefault(); setBusy(true); setErr("");
      try {
        const a = await api("/v1/accounts", { body: { type, nickname } });
        toast(`Account ${a.account_number} opened`);
        onDone();
      } catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
    }}>
      {err && <Alert>{err}</Alert>}
      <div className="grid gap-3 sm:grid-cols-2">
        {[["SAVINGS", "Savings account", "For everyday saving and receiving money."], ["CURRENT", "Current account", "For frequent payments and business use."]].map(([v, t, d]) => (
          <button type="button" key={v} onClick={() => setType(v)}
            className={cx("rounded-2xl border p-4 text-left transition-colors", type === v ? "border-brand-500 bg-brand-50 dark:bg-brand-500/10" : "border-base")}>
            <p className="font-medium">{t}</p><p className="mt-1 text-sm text-muted">{d}</p>
          </button>
        ))}
      </div>
      <Field label="Nickname (optional)"><input className="input" value={nickname} onChange={(e) => setNickname(e.target.value)} placeholder="e.g. Salary" /></Field>
      <Button loading={busy}>Open account</Button>
    </form>
  );
}

export default function OnboardingPage() {
  const profile = useApi("/v1/customers/me");
  const kyc = useApi("/v1/kyc/me");
  const accounts = useApi<{ accounts: any[] }>("/v1/accounts");
  if (profile.loading || kyc.loading || accounts.loading) return <Spinner />;

  const hasProfile = !profile.error;
  const k = kyc.data?.status ?? "NOT_SUBMITTED";
  const hasAccount = (accounts.data?.accounts ?? []).length > 0;

  return (
    <>
      <PageHeader title="Get started" subtitle="Three quick steps. Accounts can transact only after your identity is verified." />
      <div className="space-y-4">
        <Step n={1} title="Your profile" icon={UserRound} state={hasProfile ? "done" : "current"}>
          {hasProfile ? (
            <p className="text-sm text-muted">CIF <span className="font-mono">{profile.data.cif}</span> · {profile.data.full_name} · {profile.data.city}</p>
          ) : <ProfileForm onDone={() => { profile.reload(); kyc.reload(); }} />}
        </Step>

        <Step n={2} title="Identity verification (KYC)" icon={IdCard} state={k === "APPROVED" ? "done" : hasProfile ? "current" : "locked"}>
          {k === "APPROVED" && <p className="text-sm text-muted">Verified {date(kyc.data.reviewed_at)} · risk tier <Badge>{kyc.data.risk_tier}</Badge></p>}
          {k === "PENDING" && (
            <div className="space-y-3">
              <Alert tone="amber"><b>Under review.</b> An operations officer is checking your documents. You'll be notified as soon as it's done.</Alert>
              <ul className="space-y-1 text-sm">
                {(kyc.data.auto_checks ?? []).map((c: string) => (
                  <li key={c} className="flex items-center gap-2"><Clock className="h-3.5 w-3.5 text-muted" />{c}</li>
                ))}
              </ul>
              <Button variant="secondary" size="sm" onClick={kyc.reload}>Refresh status</Button>
            </div>
          )}
          {(k === "NOT_SUBMITTED" || k === "REJECTED") && (
            <>
              {k === "REJECTED" && <div className="mb-4"><Alert>Your last submission was rejected: {kyc.data.review_note}. Please resubmit.</Alert></div>}
              <KycForm dob={profile.data?.date_of_birth} onDone={kyc.reload} />
            </>
          )}
        </Step>

        <Step n={3} title="Open an account" icon={Wallet} state={hasAccount ? "done" : k === "APPROVED" ? "current" : "locked"}>
          {hasAccount ? (
            <p className="text-sm text-muted">You're all set. <Link href="/dashboard" className="font-medium text-brand-600">Go to overview →</Link></p>
          ) : <OpenAccount onDone={accounts.reload} />}
        </Step>
      </div>
    </>
  );
}
