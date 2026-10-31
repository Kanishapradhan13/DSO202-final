import Link from "next/link";
import { ArrowRight, BadgeCheck, CreditCard, Landmark, LineChart, ShieldCheck, Zap } from "lucide-react";
import { Logo } from "@/components/logo";

const features = [
  { icon: Zap, title: "Instant transfers", text: "Exactly-once transfers via an orchestrated saga with idempotency keys and automatic compensation." },
  { icon: ShieldCheck, title: "Fraud screening", text: "Every movement is scored against live rules before it touches the ledger." },
  { icon: Landmark, title: "Double-entry ledger", text: "One source of truth: debits always equal credits, provable by a live trial balance." },
  { icon: CreditCard, title: "Debit cards", text: "Issue, activate, limit and block cards; authorisations run against your available balance." },
  { icon: LineChart, title: "Loans & statements", text: "EMI schedules, one-click repayments, and PDF statements reconciled to the ledger." },
  { icon: BadgeCheck, title: "Tamper-evident audit", text: "Every state change is hash-chained in an append-only audit log." },
];

export default function Landing() {
  return (
    <main>
      <div className="bg-gradient-to-br from-brand-900 via-brand-800 to-brand-700 text-white">
        <nav className="mx-auto flex max-w-6xl items-center justify-between px-4 py-5 sm:px-6">
          <Logo light />
          <div className="flex items-center gap-2 text-sm">
            <Link href="/login" className="rounded-xl px-4 py-2 font-medium text-white/90 hover:bg-white/10">Sign in</Link>
            <Link href="/register" className="rounded-xl bg-saffron-400 px-4 py-2 font-semibold text-brand-900 hover:bg-saffron-500">Open an account</Link>
          </div>
        </nav>
        <section className="mx-auto grid max-w-6xl gap-10 px-4 pb-20 pt-10 sm:px-6 md:grid-cols-[1.2fr_1fr] md:pt-16">
          <div>
            <p className="mb-4 inline-flex rounded-full bg-white/10 px-3 py-1 text-xs font-medium text-white/80 ring-1 ring-white/20">
              DSO202 · 16 services · Kubernetes · gRPC
            </p>
            <h1 className="text-4xl font-semibold leading-tight tracking-tight sm:text-5xl">
              Banking built from <span className="text-saffron-400">fourteen</span> small, independent services.
            </h1>
            <p className="mt-5 max-w-xl text-lg text-white/75">
              Sign up, verify your identity, open accounts, move money, pay bills, use cards and take loans, with every
              ngultrum accounted for in a double-entry ledger.
            </p>
            <div className="mt-8 flex flex-wrap gap-3">
              <Link href="/register" className="inline-flex items-center gap-2 rounded-xl bg-white px-5 py-3 font-semibold text-brand-800 hover:bg-brand-50">
                Get started <ArrowRight className="h-4 w-4" />
              </Link>
              <Link href="/login" className="inline-flex items-center gap-2 rounded-xl px-5 py-3 font-medium ring-1 ring-white/30 hover:bg-white/10">
                Staff sign in
              </Link>
            </div>
          </div>
          <div className="relative hidden md:block">
            <div className="absolute -right-6 -top-6 h-48 w-48 rounded-full bg-saffron-400/20 blur-3xl" />
            <div className="relative rotate-[-4deg] rounded-3xl bg-gradient-to-br from-white/20 to-white/5 p-6 shadow-2xl ring-1 ring-white/20 backdrop-blur">
              <div className="flex items-center justify-between text-sm text-white/80">
                <span>Savings · 1010 •••• 4821</span>
                <span>VISA</span>
              </div>
              <div className="mt-10 text-xs text-white/60">Available balance</div>
              <div className="text-3xl font-semibold num">Nu. 1,24,580.00</div>
              <div className="mt-8 grid grid-cols-3 gap-2 text-center text-xs">
                {["Transfer", "Pay bill", "Card"].map((t) => (
                  <div key={t} className="rounded-xl bg-white/10 py-2">{t}</div>
                ))}
              </div>
            </div>
          </div>
        </section>
      </div>
      <section className="mx-auto max-w-6xl px-4 py-16 sm:px-6">
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {features.map((f) => (
            <div key={f.title} className="surface rounded-2xl p-5">
              <f.icon className="h-5 w-5 text-brand-600" />
              <h3 className="mt-3 font-semibold">{f.title}</h3>
              <p className="mt-1 text-sm text-muted">{f.text}</p>
            </div>
          ))}
        </div>
        <p className="mt-10 text-center text-xs text-muted">
          Demonstration system for coursework. Synthetic data only; no real money moves.
        </p>
      </section>
    </main>
  );
}
