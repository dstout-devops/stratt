// Session / auth (ADR-0090 §7). The UI is a pure /api/v1 client (§1.6): it holds no privileged
// path — the same Bearer/grant story as CLI/CI/agents. Prod auth is a JWT Bearer (oidc-client-ts,
// wired in a follow-up within the slice); dev uses the X-Stratt-Principal header the backend
// honors when OIDC is unconfigured. authHeader() is read PER REQUEST by the client middleware, so
// an identity switch takes effect on the next call with no client rebuild (gauntlet's pattern).

const DEV_PRINCIPAL_KEY = "stratt.devPrincipal";

let accessToken: string | null = null;
export function setAccessToken(t: string | null) {
  accessToken = t;
}
export function getAccessToken() {
  return accessToken;
}

export function getDevPrincipal(): string {
  return localStorage.getItem(DEV_PRINCIPAL_KEY) ?? "dev";
}
export function setDevPrincipal(id: string) {
  localStorage.setItem(DEV_PRINCIPAL_KEY, id || "dev");
}

/** authHeader is called inside the request middleware on every call. */
export function authHeader(): Record<string, string> {
  if (accessToken) return { Authorization: `Bearer ${accessToken}` };
  return { "X-Stratt-Principal": getDevPrincipal() };
}
