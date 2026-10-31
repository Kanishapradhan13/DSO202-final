"use client";

import Link from "next/link";
import { useState } from "react";
import { ChevronRight, Plus } from "lucide-react";
import { Badge, Button, Card, Empty, Money, PageHeader, Spinner } from "@/components/ui";
import { OpenAccountModal } from "@/components/open-account";
import { useApi } from "@/lib/api";
import { date, n } from "@/lib/format";
import type { Account } from "@/lib/types";

export default function AccountsPage() {
  const { data, loading, reload } = useApi<{ accounts: Account[] }>("/v1/accounts");
  const [open, setOpen] = useState(false);
  const accs = data?.accounts ?? [];
  return (
    <>
      <PageHeader title="Accounts" subtitle="Balances come straight from the double-entry ledger."
        action={<Button onClick={() => setOpen(true)}><Plus className="h-4 w-4" />New account</Button>} />
      <Card>
        {loading ? <Spinner /> : accs.length === 0 ? (
          <Empty title="No accounts yet">Complete <Link href="/onboarding" className="text-brand-600 underline">onboarding</Link> to open your first account.</Empty>
        ) : (
          <ul className="-mx-5 -my-5 divide-y divide-[var(--border)]">
            {accs.map((a) => (
              <li key={a.id}>
                <Link href={`/accounts/${a.id}`} className="flex items-center gap-4 px-5 py-4 hover:bg-[var(--surface-2)]">
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <p className="font-medium">{a.nickname}</p>
                      <Badge tone="gray">{a.type}</Badge>
                      {a.status !== "ACTIVE" && <Badge>{a.status}</Badge>}
                    </div>
                    <p className="mt-0.5 font-mono text-xs text-muted">{a.account_number} · opened {date(a.created_at, false)}</p>
                  </div>
                  <div className="text-right">
                    <Money value={a.available_balance} className="font-semibold" />
                    <p className="text-xs text-muted">{n(a.held_amount) > 0 ? <>ledger <Money value={a.balance} /></> : "available"}</p>
                  </div>
                  <ChevronRight className="h-4 w-4 text-muted" />
                </Link>
              </li>
            ))}
          </ul>
        )}
      </Card>
      <OpenAccountModal open={open} onClose={() => setOpen(false)} onDone={reload} />
    </>
  );
}
