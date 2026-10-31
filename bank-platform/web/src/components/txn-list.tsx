"use client";

import { ArrowDownLeft, ArrowUpRight, Banknote, CreditCard, HandCoins, Receipt, RotateCcw } from "lucide-react";
import { date } from "@/lib/format";
import { cx, Empty, Money } from "./ui";

export type Txn = {
  id: string;
  account_number: string;
  type: string;
  direction: "DEBIT" | "CREDIT";
  amount: string;
  balance_after: string;
  counterparty: string;
  description: string;
  reference_type: string;
  reference_id: string;
  created_at: string;
};

const icons: Record<string, any> = {
  DEPOSIT: Banknote, WITHDRAWAL: Banknote, TRANSFER_IN: ArrowDownLeft, TRANSFER_OUT: ArrowUpRight,
  PAYMENT: Receipt, CARD_PURCHASE: CreditCard, LOAN_DISBURSEMENT: HandCoins, LOAN_REPAYMENT: HandCoins, REVERSAL: RotateCcw,
};

const labels: Record<string, string> = {
  DEPOSIT: "Cash deposit", WITHDRAWAL: "Cash withdrawal", TRANSFER_IN: "Money received", TRANSFER_OUT: "Transfer sent",
  PAYMENT: "Bill payment", CARD_PURCHASE: "Card purchase", LOAN_DISBURSEMENT: "Loan credited", LOAN_REPAYMENT: "Loan repayment", REVERSAL: "Reversal",
};

export function TxnList({ items, showBalance, showAccount }: { items?: Txn[]; showBalance?: boolean; showAccount?: boolean }) {
  if (!items?.length) return <Empty title="No transactions yet">Deposits, transfers, payments and card purchases will appear here.</Empty>;
  return (
    <ul className="-mx-5 divide-y divide-[var(--border)]">
      {items.map((t) => {
        const Icon = icons[t.type] ?? ArrowUpRight;
        const credit = t.direction === "CREDIT";
        return (
          <li key={t.id} className="flex items-center gap-3 px-5 py-3">
            <div className={cx("flex h-9 w-9 shrink-0 items-center justify-center rounded-full",
              credit ? "bg-emerald-50 text-emerald-600 dark:bg-emerald-500/10" : "bg-slate-100 text-slate-600 dark:bg-slate-500/10 dark:text-slate-300")}>
              <Icon className="h-4 w-4" />
            </div>
            <div className="min-w-0 flex-1">
              <p className="truncate text-sm font-medium">{t.counterparty || labels[t.type] || t.type}</p>
              <p className="truncate text-xs text-muted">
                {labels[t.type] ?? t.type} · {t.description} · {date(t.created_at)}
                {showAccount && ` · ${t.account_number}`}
              </p>
            </div>
            <div className="text-right">
              <Money value={credit ? t.amount : -Number(t.amount)} sign className={cx("text-sm font-semibold", credit && "text-emerald-600")} />
              {showBalance && <p className="text-xs text-muted num">Bal <Money value={t.balance_after} /></p>}
            </div>
          </li>
        );
      })}
    </ul>
  );
}
