"use client";

import { CheckCircle2, CircleDashed, Clock, RotateCcw, XCircle } from "lucide-react";
import { date, titleCase } from "@/lib/format";
import { cx } from "./ui";

export type Step = { name: string; status: string; detail: string; at: string };

const icon: Record<string, any> = { OK: CheckCircle2, FAILED: XCircle, COMPENSATED: RotateCcw, WAITING: Clock };
const color: Record<string, string> = { OK: "text-emerald-600", FAILED: "text-red-600", COMPENSATED: "text-slate-500", WAITING: "text-amber-600" };

/** Renders the orchestrated saga steps recorded by transfer- or payment-service. */
export function SagaTimeline({ steps }: { steps?: Step[] }) {
  if (!steps?.length) return null;
  return (
    <ol className="relative space-y-3 border-l border-base pl-5">
      {steps.map((s, i) => {
        const Icon = icon[s.status] ?? CircleDashed;
        return (
          <li key={i} className="relative">
            <span className={cx("absolute -left-[1.72rem] top-0.5 rounded-full bg-[var(--surface)]", color[s.status])}>
              <Icon className="h-4 w-4" />
            </span>
            <p className="text-sm font-medium">
              {titleCase(s.name)} <span className={cx("ml-1 text-xs font-normal", color[s.status])}>{titleCase(s.status)}</span>
            </p>
            {s.detail && <p className="break-all text-xs text-muted">{s.detail}</p>}
            <p className="text-[0.7rem] text-muted">{date(s.at)}</p>
          </li>
        );
      })}
    </ol>
  );
}
