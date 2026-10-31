/** Money is int64 chhertrum on the wire (JSON string); Nu. 1 = 100 Ch. */
export function n(v: unknown): number {
  const x = Number(v ?? 0);
  return Number.isFinite(x) ? x : 0;
}

const nf = new Intl.NumberFormat("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 });

export function nu(minor: unknown, opts: { sign?: boolean } = {}) {
  const v = n(minor);
  const s = `Nu. ${nf.format(Math.abs(v) / 100)}`;
  if (v < 0) return `−${s}`;
  return opts.sign && v > 0 ? `+${s}` : s;
}

/** Parses "1,234.50" into chhertrum without floating-point drift. */
export function parseNu(input: string): number | null {
  const s = input.replace(/[,\s]/g, "").replace(/^Nu\.?/i, "");
  if (!/^\d+(\.\d{0,2})?$/.test(s)) return null;
  const [whole, frac = ""] = s.split(".");
  const v = Number(whole) * 100 + Number((frac + "00").slice(0, 2));
  return v > 0 ? v : null;
}

export function date(v?: string, withTime = true) {
  if (!v) return "—";
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return v;
  return d.toLocaleString("en-GB", {
    day: "numeric", month: "short", year: "numeric",
    ...(withTime ? { hour: "2-digit", minute: "2-digit" } : {}),
  });
}

export function relTime(v?: string) {
  if (!v) return "";
  const diff = (Date.now() - new Date(v).getTime()) / 1000;
  if (diff < 60) return "just now";
  if (diff < 3600) return `${Math.floor(diff / 60)} min ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)} h ago`;
  return date(v, false);
}

export function titleCase(s?: string) {
  return (s ?? "").toLowerCase().replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
}

export function today(offsetDays = 0) {
  const d = new Date(Date.now() + offsetDays * 86400000);
  return d.toISOString().slice(0, 10);
}

export const STAFF_ROLES = ["ops_officer", "loan_officer", "fraud_analyst", "auditor", "admin"];
export function hasRole(roles: string[] | undefined, ...want: string[]) {
  if (!roles) return false;
  return roles.includes("admin") || want.some((r) => roles.includes(r));
}
