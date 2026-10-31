// Server-only helpers: the portal is a backend-for-frontend. Access and
// refresh tokens live in httpOnly cookies and never reach browser JavaScript.
import { cookies } from "next/headers";
import { NextResponse } from "next/server";

export const GATEWAY_URL = process.env.GATEWAY_URL ?? "http://localhost:8080";
const SECURE = (process.env.COOKIE_SECURE ?? "false") === "true";

export const ACCESS_COOKIE = "cbmp_at";
export const REFRESH_COOKIE = "cbmp_rt";

export type TokenPair = { access_token: string; refresh_token: string; expires_in: string | number };

export function setSession(res: NextResponse, t: TokenPair) {
  res.cookies.set(ACCESS_COOKIE, t.access_token, {
    httpOnly: true, sameSite: "lax", secure: SECURE, path: "/", maxAge: Number(t.expires_in) || 900,
  });
  res.cookies.set(REFRESH_COOKIE, t.refresh_token, {
    httpOnly: true, sameSite: "lax", secure: SECURE, path: "/", maxAge: 7 * 24 * 3600,
  });
}

export function clearSession(res: NextResponse) {
  res.cookies.set(ACCESS_COOKIE, "", { path: "/", maxAge: 0 });
  res.cookies.set(REFRESH_COOKIE, "", { path: "/", maxAge: 0 });
}

export async function readSession() {
  const jar = await cookies();
  return { access: jar.get(ACCESS_COOKIE)?.value, refresh: jar.get(REFRESH_COOKIE)?.value };
}

export function forwardedFor(req: Request): string {
  const xff = req.headers.get("x-forwarded-for");
  return xff?.split(",")[0].trim() || req.headers.get("x-real-ip") || "127.0.0.1";
}

export async function gatewayJSON(path: string, init: RequestInit & { ip?: string } = {}) {
  const headers = new Headers(init.headers);
  headers.set("Content-Type", "application/json");
  if (init.ip) headers.set("X-Forwarded-For", init.ip);
  const res = await fetch(GATEWAY_URL + path, { ...init, headers, cache: "no-store" });
  const text = await res.text();
  let body: any = {};
  try {
    body = text ? JSON.parse(text) : {};
  } catch {
    body = { message: text };
  }
  return { status: res.status, body };
}
