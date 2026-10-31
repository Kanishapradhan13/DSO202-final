"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import {
  ArrowLeftRight, Bell, BookCheck, CreditCard, FileSearch, Gauge, HandCoins, Landmark, LayoutDashboard, LogOut,
  Menu, Receipt, Settings, ShieldAlert, UserCheck, Users, Wallet, X,
} from "lucide-react";
import { Logo } from "@/components/logo";
import { cx, Spinner } from "@/components/ui";
import { MeContext, Me } from "@/components/me";
import { session, useApi } from "@/lib/api";
import { hasRole, relTime } from "@/lib/format";

type NavItem = { href: string; label: string; icon: any; show?: (r: string[]) => boolean };

const customerNav: NavItem[] = [
  { href: "/dashboard", label: "Overview", icon: LayoutDashboard },
  { href: "/accounts", label: "Accounts", icon: Wallet },
  { href: "/transfer", label: "Transfer", icon: ArrowLeftRight },
  { href: "/payments", label: "Pay bills", icon: Receipt },
  { href: "/cards", label: "Cards", icon: CreditCard },
  { href: "/loans", label: "Loans", icon: HandCoins },
];

const staffNav: NavItem[] = [
  { href: "/staff", label: "Back office", icon: Gauge, show: (r) => hasRole(r, "ops_officer", "loan_officer", "fraud_analyst", "auditor") },
  { href: "/staff/kyc", label: "KYC reviews", icon: UserCheck, show: (r) => hasRole(r, "ops_officer", "loan_officer", "auditor") },
  { href: "/staff/customers", label: "Customers & teller", icon: Users, show: (r) => hasRole(r, "ops_officer", "loan_officer", "fraud_analyst", "auditor") },
  { href: "/staff/loans", label: "Loan desk", icon: Landmark, show: (r) => hasRole(r, "loan_officer", "auditor") },
  { href: "/staff/fraud", label: "Fraud & risk", icon: ShieldAlert, show: (r) => hasRole(r, "fraud_analyst", "auditor") },
  { href: "/staff/audit", label: "Audit & ledger", icon: FileSearch, show: (r) => hasRole(r, "auditor", "fraud_analyst") },
  { href: "/staff/users", label: "Users & roles", icon: BookCheck, show: (r) => hasRole(r, "admin") },
];

function NavLinks({ roles, onNavigate }: { roles: string[]; onNavigate?: () => void }) {
  const path = usePathname();
  const isCustomer = roles.includes("customer");
  const staff = staffNav.filter((i) => i.show?.(roles));
  const link = (i: NavItem) => {
    const active = path === i.href || (i.href !== "/staff" && path.startsWith(i.href + "/"));
    return (
      <Link key={i.href} href={i.href} onClick={onNavigate}
        className={cx("flex items-center gap-3 rounded-xl px-3 py-2 text-sm font-medium transition-colors",
          active ? "bg-brand-700 text-white shadow-sm" : "text-muted hover:bg-[var(--surface-2)] hover:text-[var(--text)]")}>
        <i.icon className="h-4 w-4" /> {i.label}
      </Link>
    );
  };
  return (
    <nav className="space-y-6">
      {isCustomer && <div className="space-y-1">{customerNav.map(link)}</div>}
      {staff.length > 0 && (
        <div className="space-y-1">
          <p className="px-3 pb-1 text-[0.7rem] font-semibold uppercase tracking-wider text-muted">Staff</p>
          {staff.map(link)}
        </div>
      )}
      <div className="space-y-1">
        {link({ href: "/notifications", label: "Notifications", icon: Bell })}
        {link({ href: "/settings", label: "Settings", icon: Settings })}
      </div>
    </nav>
  );
}

function Bell_() {
  const { data, reload } = useApi<{ notifications: any[]; unread_count: string }>("/v1/notifications?page_size=6");
  const [open, setOpen] = useState(false);
  useEffect(() => {
    const t = setInterval(reload, 15000);
    return () => clearInterval(t);
  }, [reload]);
  const unread = Number(data?.unread_count ?? 0);
  return (
    <div className="relative">
      <button onClick={() => setOpen(!open)} className="relative rounded-xl p-2 hover:bg-[var(--surface-2)]" aria-label="Notifications">
        <Bell className="h-5 w-5" />
        {unread > 0 && (
          <span className="absolute right-1 top-1 flex h-4 min-w-4 items-center justify-center rounded-full bg-red-600 px-1 text-[0.6rem] font-bold text-white">
            {unread > 9 ? "9+" : unread}
          </span>
        )}
      </button>
      {open && (
        <div className="surface fade-in absolute right-0 z-40 mt-2 w-[min(360px,calc(100vw-2rem))] rounded-2xl shadow-xl" onMouseLeave={() => setOpen(false)}>
          <div className="flex items-center justify-between border-b border-base px-4 py-3 text-sm font-semibold">
            Notifications
            <Link href="/notifications" onClick={() => setOpen(false)} className="text-xs font-medium text-brand-600">View all</Link>
          </div>
          <ul className="max-h-96 divide-y divide-[var(--border)] overflow-y-auto">
            {(data?.notifications ?? []).length === 0 && <li className="px-4 py-6 text-center text-sm text-muted">You're all caught up.</li>}
            {(data?.notifications ?? []).map((n) => (
              <li key={n.id} className={cx("px-4 py-3", !n.read && "bg-brand-50/50 dark:bg-brand-500/5")}>
                <p className="text-sm font-medium">{n.title}</p>
                <p className="mt-0.5 line-clamp-2 text-xs text-muted">{n.body}</p>
                <p className="mt-1 text-[0.7rem] text-muted">{relTime(n.created_at)}</p>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

export default function AppLayout({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const { data: me, reload, error } = useApi<Me>("/v1/auth/me");
  const [mobile, setMobile] = useState(false);
  const path = usePathname();
  useEffect(() => setMobile(false), [path]);

  async function logout() {
    await session("logout");
    router.replace("/login");
    router.refresh();
  }

  if (!me) {
    return <div className="flex min-h-screen items-center justify-center">{error ? <p className="text-sm text-muted">{error.message}</p> : <Spinner />}</div>;
  }

  const sidebar = (
    <div className="flex h-full flex-col">
      <div className="px-5 py-5">
        <Link href={me.roles.includes("customer") ? "/dashboard" : "/staff"}><Logo /></Link>
      </div>
      <div className="flex-1 overflow-y-auto px-3"><NavLinks roles={me.roles} /></div>
      <div className="border-t border-base p-3">
        <div className="flex items-center gap-3 rounded-xl px-2 py-2">
          <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-brand-100 text-sm font-semibold text-brand-700">
            {me.full_name.split(" ").map((p) => p[0]).slice(0, 2).join("")}
          </div>
          <div className="min-w-0 flex-1">
            <p className="truncate text-sm font-medium">{me.full_name}</p>
            <p className="truncate text-xs text-muted">{me.roles.map((r) => r.replace("_", " ")).join(", ")}</p>
          </div>
          <button onClick={logout} className="rounded-lg p-2 text-muted hover:bg-[var(--surface-2)]" title="Sign out" aria-label="Sign out">
            <LogOut className="h-4 w-4" />
          </button>
        </div>
      </div>
    </div>
  );

  return (
    <MeContext.Provider value={{ me, reload }}>
      <div className="min-h-screen lg:grid lg:grid-cols-[260px_1fr]">
        <aside className="surface sticky top-0 hidden h-screen border-y-0 border-l-0 lg:block">{sidebar}</aside>
        {mobile && (
          <div className="fixed inset-0 z-50 bg-slate-950/50 lg:hidden" onClick={() => setMobile(false)}>
            <aside className="surface fade-in h-full w-72" onClick={(e) => e.stopPropagation()}>
              <button className="absolute right-3 top-4 rounded-lg p-2" onClick={() => setMobile(false)} aria-label="Close menu"><X className="h-5 w-5" /></button>
              {sidebar}
            </aside>
          </div>
        )}
        <div className="min-w-0">
          <header className="sticky top-0 z-30 flex items-center justify-between gap-3 border-b border-base bg-[var(--bg)]/85 px-4 py-3 backdrop-blur sm:px-8">
            <div className="flex items-center gap-2">
              <button className="rounded-lg p-2 lg:hidden" onClick={() => setMobile(true)} aria-label="Open menu"><Menu className="h-5 w-5" /></button>
              <div className="lg:hidden"><Logo /></div>
              <p className="hidden text-sm text-muted lg:block">
                Kuzuzangpo, <span className="font-medium text-[var(--text)]">{me.full_name.split(" ")[0]}</span>
              </p>
            </div>
            <Bell_ />
          </header>
          <main className="mx-auto max-w-6xl px-4 py-6 sm:px-8 sm:py-8">{children}</main>
        </div>
      </div>
    </MeContext.Provider>
  );
}
