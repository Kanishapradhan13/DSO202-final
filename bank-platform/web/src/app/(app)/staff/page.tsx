"use client";

import Link from "next/link";
import { FileSearch, Landmark, Scale, ShieldAlert, UserCheck } from "lucide-react";
import { Badge, Card, PageHeader, Stat } from "@/components/ui";
import { useMe } from "@/components/me";
import { useApi } from "@/lib/api";
import { hasRole, nu } from "@/lib/format";

export default function StaffHome() {
  const { me } = useMe();
  const r = me?.roles ?? [];
  const kyc = useApi<{ cases: any[] }>(hasRole(r, "ops_officer", "loan_officer", "auditor") ? "/v1/staff/kyc?status=PENDING&page_size=100" : null);
  const loans = useApi<{ loans: any[] }>(hasRole(r, "loan_officer", "auditor") ? "/v1/staff/loans?status=APPLIED&page_size=100" : null);
  const fraud = useApi<any>(hasRole(r, "fraud_analyst", "auditor") ? "/v1/staff/fraud/stats" : null);
  const tb = useApi<any>(hasRole(r, "auditor") ? "/v1/staff/ledger/trial-balance" : null);

  const tiles = [
    hasRole(r, "ops_officer", "loan_officer", "auditor") && { href: "/staff/kyc", icon: UserCheck, label: "KYC awaiting review", value: kyc.data?.cases?.length ?? "…" },
    hasRole(r, "loan_officer", "auditor") && { href: "/staff/loans", icon: Landmark, label: "Loan applications", value: loans.data?.loans?.length ?? "…" },
    hasRole(r, "fraud_analyst", "auditor") && { href: "/staff/fraud", icon: ShieldAlert, label: "Open fraud alerts", value: fraud.data ? Number(fraud.data.open_alerts) : "…" },
    hasRole(r, "auditor") && { href: "/staff/audit", icon: Scale, label: "Ledger", value: tb.data ? (tb.data.balanced ? "Balanced" : "UNBALANCED") : "…" },
  ].filter(Boolean) as any[];

  return (
    <>
      <PageHeader title="Back office" subtitle="Work queues for your roles. Every action you take is written to the audit trail." />
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        {tiles.map((t) => (
          <Link key={t.href} href={t.href} className="transition-transform hover:-translate-y-0.5">
            <Stat label={t.label} value={t.value} icon={<t.icon className="h-4 w-4" />} />
          </Link>
        ))}
      </div>
      {tb.data && (
        <Card className="mt-6" title="Ledger at a glance">
          <div className="grid gap-4 sm:grid-cols-4">
            <div><p className="text-xs text-muted">Journal entries</p><p className="text-lg font-semibold num">{Number(tb.data.entry_count).toLocaleString()}</p></div>
            <div><p className="text-xs text-muted">Total debits</p><p className="text-lg font-semibold num">{nu(tb.data.total_debits)}</p></div>
            <div><p className="text-xs text-muted">Total credits</p><p className="text-lg font-semibold num">{nu(tb.data.total_credits)}</p></div>
            <div><p className="text-xs text-muted">Customer deposits</p><p className="text-lg font-semibold num">{nu(tb.data.customer_funds)}</p></div>
          </div>
          <p className="mt-4 text-sm">Integrity: <Badge>{tb.data.balanced ? "OK" : "FAILED"}</Badge> <span className="text-muted">debits = credits, every entry balanced, every account matches its postings.</span></p>
        </Card>
      )}
      {tiles.length === 0 && (
        <Card><p className="text-sm text-muted">Your role has no back-office queues. <Link href="/staff/users" className="text-brand-600"><FileSearch className="inline h-4 w-4" /> Manage users</Link></p></Card>
      )}
    </>
  );
}
