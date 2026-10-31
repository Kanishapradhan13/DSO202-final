"use client";

import { Bell, CheckCheck } from "lucide-react";
import { Button, Card, cx, Empty, PageHeader, Spinner } from "@/components/ui";
import { api, useApi } from "@/lib/api";
import { date } from "@/lib/format";

export default function NotificationsPage() {
  const { data, loading, reload } = useApi<{ notifications: any[]; unread_count: string }>("/v1/notifications?page_size=100");
  const markAll = async () => { await api("/v1/notifications/all/read", { body: {} }); reload(); };
  const markOne = async (id: string) => { await api(`/v1/notifications/${id}/read`, { body: {} }); reload(); };
  return (
    <>
      <PageHeader title="Notifications" subtitle={`${Number(data?.unread_count ?? 0)} unread`}
        action={<Button variant="secondary" onClick={markAll}><CheckCheck className="h-4 w-4" />Mark all read</Button>} />
      <Card>
        {loading ? <Spinner /> : !data?.notifications?.length ? <Empty title="Nothing here yet" icon={<Bell className="h-5 w-5" />} /> : (
          <ul className="-mx-5 -my-5 divide-y divide-[var(--border)]">
            {data.notifications.map((n) => (
              <li key={n.id} className={cx("flex gap-3 px-5 py-4", !n.read && "bg-brand-50/50 dark:bg-brand-500/5")} onClick={() => !n.read && markOne(n.id)}>
                <span className={cx("mt-1.5 h-2 w-2 shrink-0 rounded-full", n.read ? "bg-transparent" : "bg-brand-500")} />
                <div className="min-w-0 flex-1">
                  <p className="text-sm font-medium">{n.title}</p>
                  <p className="text-sm text-muted">{n.body}</p>
                  <p className="mt-1 text-xs text-muted">{date(n.created_at)}</p>
                </div>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </>
  );
}
