// Authenticated proxy to the API gateway. Adds the bearer token from the
// httpOnly cookie and transparently refreshes it once on 401.
import { NextRequest, NextResponse } from "next/server";
import { GATEWAY_URL, clearSession, forwardedFor, gatewayJSON, readSession, setSession, TokenPair } from "@/lib/server";

export const dynamic = "force-dynamic";

async function forward(req: NextRequest, path: string, token?: string, body?: ArrayBuffer) {
  const headers = new Headers();
  for (const h of ["content-type", "idempotency-key", "x-request-id", "accept"]) {
    const v = req.headers.get(h);
    if (v) headers.set(h, v);
  }
  headers.set("X-Forwarded-For", forwardedFor(req));
  if (token) headers.set("Authorization", `Bearer ${token}`);
  return fetch(`${GATEWAY_URL}/${path}${req.nextUrl.search}`, {
    method: req.method,
    headers,
    body: body && body.byteLength > 0 ? body : undefined,
    cache: "no-store",
    redirect: "manual",
  });
}

async function handle(req: NextRequest, { params }: { params: Promise<{ path: string[] }> }) {
  const { path } = await params;
  const target = path.join("/");
  if (!target.startsWith("v1/")) {
    return NextResponse.json({ message: "not found" }, { status: 404 });
  }
  // CSRF defence in depth (cookies are already SameSite=Lax): state-changing
  // requests must come from this origin.
  const origin = req.headers.get("origin");
  if (req.method !== "GET" && origin && new URL(origin).host !== req.headers.get("host")) {
    return NextResponse.json({ message: "cross-origin request rejected" }, { status: 403 });
  }
  const body = ["GET", "HEAD"].includes(req.method) ? undefined : await req.arrayBuffer();
  const { access, refresh } = await readSession();
  let res = await forward(req, target, access, body);
  let refreshed: TokenPair | undefined;

  if (res.status === 401 && refresh) {
    const r = await gatewayJSON("/v1/auth/refresh", {
      method: "POST", body: JSON.stringify({ refresh_token: refresh }), ip: forwardedFor(req),
    });
    if (r.status === 200 && r.body?.tokens?.access_token) {
      refreshed = r.body.tokens as TokenPair;
      res = await forward(req, target, refreshed.access_token, body);
    }
  }

  const out = new NextResponse(res.body, { status: res.status });
  for (const h of ["content-type", "x-request-id", "retry-after"]) {
    const v = res.headers.get(h);
    if (v) out.headers.set(h, v);
  }
  if (target.endsWith("/download")) {
    const fmt = req.nextUrl.searchParams.get("format") === "csv" ? "csv" : "pdf";
    out.headers.set("Content-Disposition", `attachment; filename="statement-${path[2] ?? "download"}.${fmt}"`);
  }
  if (refreshed) setSession(out, refreshed);
  else if (res.status === 401 && refresh) clearSession(out);
  return out;
}

export { handle as GET, handle as POST, handle as PATCH, handle as DELETE, handle as PUT };
