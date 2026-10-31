// Session endpoints: they exchange credentials for tokens and keep them in
// httpOnly cookies. Only non-secret fields are returned to the browser.
import { NextRequest, NextResponse } from "next/server";
import { clearSession, forwardedFor, gatewayJSON, readSession, setSession } from "@/lib/server";

export const dynamic = "force-dynamic";

export async function POST(req: NextRequest, { params }: { params: Promise<{ action: string }> }) {
  const { action } = await params;
  const ip = forwardedFor(req);
  const input = await req.json().catch(() => ({}));

  if (action === "logout") {
    const { refresh } = await readSession();
    if (refresh) await gatewayJSON("/v1/auth/logout", { method: "POST", body: JSON.stringify({ refresh_token: refresh }), ip });
    const res = NextResponse.json({ ok: true });
    clearSession(res);
    return res;
  }

  const path = action === "login" ? "/v1/auth/login" : action === "mfa" ? "/v1/auth/mfa/verify" : null;
  if (!path) return NextResponse.json({ message: "not found" }, { status: 404 });

  const r = await gatewayJSON(path, { method: "POST", body: JSON.stringify(input), ip });
  if (r.status !== 200) return NextResponse.json(r.body, { status: r.status });

  const res = NextResponse.json({
    mfa_required: !!r.body.mfa_required,
    mfa_token: r.body.mfa_token || undefined,
    user: r.body.user ?? null,
  });
  if (r.body.tokens?.access_token) setSession(res, r.body.tokens);
  return res;
}
