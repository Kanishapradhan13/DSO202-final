"use client";

import Link from "next/link";
import { ArrowLeftRight, Check, CreditCard, HandCoins, Plus, Receipt, Wallet } from "lucide-react";
import { Badge, Card, cx, Money, PageHeader, Spinner, Stat } from "@/components/ui";
import { TxnList, Txn } from "@/components/txn-list";
import { useMe } from "@/components/me";
import { useApi } from "@/lib/api";
import { date, n, nu } from "@/lib/format";
import type { Account } from "@/lib/types";

function Onboarding({ profile, kyc, accounts }: { profile: boolean; kyc?: string; accounts: number }) {
  const steps = [
    { label: "Create your profile", done: profile },
    { label: "Verify your identity (KYC)", done: kyc === "APPROVED", note: kyc === "PENDING" ? "Under review" : kyc === "REJECTED" ? "Needs attention" : undefined },
    { label: "Open your first account", done: accounts > 0 },
  ];
  if (steps.every((s) => s.done)) return null;
  return (
    <Card className="mb-6 overflow-hidden border-brand-200 bg-gradient-to-r from-brand-50 to-transparent dark:from-brand-500/10">
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div>
          <h2 className="font-semibold">Finish setting up your account</h2>
          <ol className="mt-3 flex flex-wrap gap-x-6 gap-y-2 text-sm">
            {steps.map((s, i) => (
              <li key={s.label} className="flex items-center gap-2">
                <span className={cx("flex h-5 w-5 items-center justify-center rounded-full text-[0.7rem] font-bold",
                  s.done ? "bg-emerald-600 text-white" : "bg-white text-brand-700 ring-1 ring-brand-200 dark:bg-transparent")}>
                  {s.done ? <Check className="h-3 w-3" /> : i + 1}
                </span>
                <span className={s.done ? "text-muted line-through" : ""}>{s.label}</span>
                {s.note && <Badge tone="amber">{s.note}</Badge>}
              </li>
            ))}
          </ol>
        </div>
        <Link href="/onboarding" className="rounded-xl bg-brand-700 px-4 py-2.5 text-sm font-medium text-white hover:bg-brand-800">Continue</Link>
      </div>
    </Card>
  );
}

const actions = [
  { href: "/transfer", label: "Transfer", icon: ArrowLeftRight },
  { href: "/payments", label: "Pay a bill", icon: Receipt },
  { href: "/cards", label: "Cards", icon: CreditCard },
  { href: "/loans", label: "Loans", icon: HandCoins },
];

export default function Dashboard() {
  const { me } = useMe();
  const profile = useApi("/v1/customers/me");
  const kyc = useApi<{ status: string }>("/v1/kyc/me");
  const accounts = useApi<{ accounts: Account[] }>("/v1/accounts");
  const txns = useApi<{ transactions: Txn[] }>("/v1/transactions?page_size=8");
  const loans = useApi<{ loans: any[] }>("/v1/loans");

  const accs = accounts.data?.accounts ?? [];
  const total = accs.reduce((s, a) => s + n(a.balance), 0);
  const available = accs.reduce((s, a) => s + n(a.available_balance), 0);
  const activeLoans = (loans.data?.loans ?? []).filter((l) => l.status === "ACTIVE");
  const nextDue = activeLoans.filter((l) => l.next_due_date).sort((a, b) => a.next_due_date.localeCompare(b.next_due_date))[0];

  return (
    <>
      <PageHeader title="Overview" subtitle={me?.last_login_at ? `Last sign-in ${date(me.last_login_at)}` : undefined} />
      {!profile.loading && !kyc.loading && !accounts.loading && (
        <Onboarding profile={!profile.error} kyc={kyc.data?.status} accounts={accs.length} />
      )}

      <div className="grid gap-4 sm:grid-cols-3">
        <Stat label="Total balance" value={nu(total)} hint={`${accs.length} account${accs.length === 1 ? "" : "s"}`} icon={<Wallet className="h-4 w-4" />} />
        <Stat label="Available to spend" value={nu(available)} hint={total !== available ? `${nu(total - available)} on hold` : "No funds on hold"} />
        <Stat label="Next EMI" value={nextDue ? nu(nextDue.next_due_amount) : "—"} hint={nextDue ? `${nextDue.product_name} · due ${date(nextDue.next_due_date, false)}` : "No active loans"} icon={<HandCoins className="h-4 w-4" />} />
      </div>

      <div className="mt-6 grid grid-cols-4 gap-3">
        {actions.map((a) => (
          <Link key={a.href} href={a.href} className="surface flex flex-col items-center gap-2 rounded-2xl py-4 text-sm font-medium hover:border-brand-200 hover:text-brand-700">
            <span className="rounded-full bg-brand-50 p-2.5 text-brand-700 dark:bg-brand-500/10 dark:text-brand-200"><a.icon className="h-5 w-5" /></span>
            {a.label}
          </Link>
        ))}
      </div>

      <div className="mt-6 grid gap-6 lg:grid-cols-[1fr_1.3fr]">
        <Card title="Accounts" action={<Link href="/accounts" className="text-sm font-medium text-brand-600">Manage</Link>}>
          {accounts.loading ? <Spinner /> : accs.length === 0 ? (
            <div className="py-6 text-center text-sm text-muted">
              No accounts yet.{" "}
              <Link href="/onboarding" className="inline-flex items-center gap-1 font-medium text-brand-600"><Plus className="h-3.5 w-3.5" />Open one</Link>
            </div>
          ) : (
            <div className="space-y-3">
              {accs.map((a) => (
                <Link key={a.id} href={`/accounts/${a.id}`} className="block rounded-2xl bg-gradient-to-br from-brand-800 to-brand-600 p-4 text-white shadow-sm transition-transform hover:-translate-y-0.5">
                  <div className="flex items-center justify-between text-xs text-white/75">
                    <span>{a.nickname} · {a.type.toLowerCase()}</span>
                    {a.status !== "ACTIVE" ? <Badge>{a.status}</Badge> : <span className="font-mono">{a.account_number}</span>}
                  </div>
                  <div className="mt-4 text-2xl font-semibold num">{nu(a.available_balance)}</div>
                  <div className="text-xs text-white/70">
                    available{n(a.held_amount) > 0 && ` · ${nu(a.held_amount)} on hold`}
                  </div>
                </Link>
              ))}
            </div>
          )}
        </Card>
        <Card title="Recent activity">
          {txns.loading ? <Spinner /> : <TxnList items={txns.data?.transactions} showAccount />}
        </Card>
      </div>
    </>
  );
}
