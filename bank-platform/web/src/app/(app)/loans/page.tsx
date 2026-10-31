"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { ChevronRight, Calculator } from "lucide-react";
import { Alert, Badge, Button, Card, cx, Empty, Field, Money, PageHeader, Spinner, useToast } from "@/components/ui";
import { api, errMsg, useApi } from "@/lib/api";
import { date, n, nu, parseNu } from "@/lib/format";
import type { Account } from "@/lib/types";

type Product = { id: string; name: string; annual_rate_bps: number; min_amount: string; max_amount: string; min_term_months: number; max_term_months: number; description: string };

export default function LoansPage() {
  const toast = useToast();
  const products = useApi<{ products: Product[] }>("/v1/loans/products");
  const loans = useApi<{ loans: any[] }>("/v1/loans");
  const accounts = useApi<{ accounts: Account[] }>("/v1/accounts");
  const accs = (accounts.data?.accounts ?? []).filter((a) => a.status === "ACTIVE");

  const [pid, setPid] = useState("personal");
  const [amount, setAmount] = useState("100000");
  const [term, setTerm] = useState(24);
  const [income, setIncome] = useState("");
  const [purpose, setPurpose] = useState("");
  const [acct, setAcct] = useState("");
  const [quote, setQuote] = useState<any>(null);
  const [qerr, setQerr] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const product = products.data?.products.find((p) => p.id === pid);
  const minor = parseNu(amount);
  useEffect(() => { if (!acct && accs[0]) setAcct(accs[0].id); }, [accs, acct]);
  useEffect(() => {
    if (product) setTerm((t) => Math.min(Math.max(t, product.min_term_months), product.max_term_months));
  }, [product]);
  useEffect(() => {
    if (!minor || !product) return;
    const t = setTimeout(async () => {
      try { setQuote(await api(`/v1/loans/quote?product_id=${pid}&amount=${minor}&term_months=${term}`)); setQerr(""); }
      catch (e) { setQuote(null); setQerr(errMsg(e)); }
    }, 300);
    return () => clearTimeout(t);
  }, [pid, minor, term, product]);

  const incomeMinor = parseNu(income) ?? 0;
  const dti = quote && incomeMinor ? (n(quote.emi) / incomeMinor) * 100 : 0;

  async function apply(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true); setErr("");
    try {
      await api("/v1/loans", { body: { product_id: pid, amount: String(minor), term_months: term, disbursement_account_id: acct, purpose, monthly_income: String(incomeMinor) } });
      toast("Application submitted. A loan officer will review it.");
      loans.reload(); setPurpose("");
    } catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
  }

  return (
    <>
      <PageHeader title="Loans" subtitle="Fixed rate, fixed term, equal monthly instalments." />
      <div className="grid gap-6 lg:grid-cols-[1.2fr_1fr]">
        <Card title={<span className="inline-flex items-center gap-2"><Calculator className="h-4 w-4" />Apply for a loan</span>}>
          {products.loading ? <Spinner /> : (
            <form className="space-y-4" onSubmit={apply}>
              <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
                {products.data?.products.map((p) => (
                  <button type="button" key={p.id} onClick={() => setPid(p.id)}
                    className={cx("rounded-xl border p-3 text-left", pid === p.id ? "border-brand-500 bg-brand-50 dark:bg-brand-500/10" : "border-base")}>
                    <p className="text-sm font-medium">{p.name}</p>
                    <p className="text-xs text-muted">{(p.annual_rate_bps / 100).toFixed(2)}% p.a.</p>
                  </button>
                ))}
              </div>
              {product && <p className="text-sm text-muted">{product.description} {nu(product.min_amount)} to {nu(product.max_amount)}, {product.min_term_months}–{product.max_term_months} months.</p>}
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label="Amount (Nu.)" error={qerr || undefined}><input className="input num" inputMode="decimal" value={amount} onChange={(e) => setAmount(e.target.value)} /></Field>
                <Field label={`Term: ${term} months`}>
                  <input type="range" className="mt-3 w-full accent-[var(--color-brand-600)]" min={product?.min_term_months ?? 6} max={product?.max_term_months ?? 60} value={term} onChange={(e) => setTerm(Number(e.target.value))} />
                </Field>
                <Field label="Monthly income (Nu.)" hint={dti ? `EMI is ${dti.toFixed(0)}% of income${dti > 50 ? " (above the 50% guideline)" : ""}` : "Used for the affordability check"}>
                  <input className="input num" inputMode="decimal" required value={income} onChange={(e) => setIncome(e.target.value)} />
                </Field>
                <Field label="Disburse to">
                  <select className="input" value={acct} onChange={(e) => setAcct(e.target.value)}>
                    {accs.map((a) => <option key={a.id} value={a.id}>{a.nickname} · {a.account_number}</option>)}
                  </select>
                </Field>
              </div>
              <Field label="Purpose"><input className="input" value={purpose} onChange={(e) => setPurpose(e.target.value)} placeholder="e.g. Laptop for studies" /></Field>
              {quote && (
                <div className="grid grid-cols-3 gap-3 rounded-2xl bg-[var(--surface-2)] p-4 text-center">
                  <div><p className="text-xs text-muted">Monthly EMI</p><p className="text-lg font-semibold num">{nu(quote.emi)}</p></div>
                  <div><p className="text-xs text-muted">Total interest</p><p className="text-lg font-semibold num">{nu(quote.total_interest)}</p></div>
                  <div><p className="text-xs text-muted">Total payable</p><p className="text-lg font-semibold num">{nu(quote.total_payable)}</p></div>
                </div>
              )}
              {err && <Alert>{err}</Alert>}
              {!accs.length && <Alert tone="amber">You need an active account to receive the loan.</Alert>}
              <Button className="w-full" loading={busy} disabled={!quote || !acct || !incomeMinor}>Submit application</Button>
            </form>
          )}
        </Card>
        <Card title="My loans">
          {loans.loading ? <Spinner /> : !loans.data?.loans?.length ? <Empty title="No loans yet">Applications you submit appear here.</Empty> : (
            <ul className="-mx-5 -my-5 divide-y divide-[var(--border)]">
              {loans.data.loans.map((l) => (
                <li key={l.id}>
                  <Link href={`/loans/${l.id}`} className="flex items-center gap-3 px-5 py-4 hover:bg-[var(--surface-2)]">
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2"><p className="font-medium">{l.product_name}</p><Badge>{l.status}</Badge></div>
                      <p className="text-xs text-muted">
                        <Money value={l.principal} /> · {l.term_months} mo · EMI <Money value={l.emi} />
                        {l.status === "ACTIVE" && l.next_due_date && <> · next due {date(l.next_due_date, false)}</>}
                      </p>
                      {l.status === "ACTIVE" && (
                        <div className="mt-2 h-1.5 overflow-hidden rounded-full bg-[var(--surface-2)]">
                          <div className="h-full bg-emerald-500" style={{ width: `${(l.paid_instalments / l.term_months) * 100}%` }} />
                        </div>
                      )}
                    </div>
                    <ChevronRight className="h-4 w-4 text-muted" />
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>
    </>
  );
}
