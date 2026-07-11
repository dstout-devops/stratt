// Hand-rolled OIDC authorization-code + PKCE (ADR-0012): ~100 lines beats a
// dependency for one well-understood flow. The access token is a JWT the API
// verifies via JWKS (ADR-0009 slice 5) — one Bearer works for UI and curl
// alike (§1.6). Tokens live in sessionStorage (dev posture; revisit for
// production hardening in the Helm slice).

const ISSUER = import.meta.env.VITE_OIDC_ISSUER as string | undefined;
const CLIENT_ID = import.meta.env.VITE_OIDC_CLIENT_ID as string | undefined;

export const oidcConfigured = Boolean(ISSUER && CLIENT_ID);

type Tokens = {
  accessToken: string;
  idClaims: Record<string, unknown>;
  expiresAt: number;
};

const KEY = "stratt.tokens";
const VERIFIER_KEY = "stratt.pkce_verifier";

export function currentTokens(): Tokens | null {
  const raw = sessionStorage.getItem(KEY);
  if (!raw) return null;
  const t = JSON.parse(raw) as Tokens;
  if (t.expiresAt < Date.now() / 1000 + 10) {
    sessionStorage.removeItem(KEY);
    return null;
  }
  return t;
}

function b64url(bytes: Uint8Array): string {
  let s = "";
  bytes.forEach((b) => (s += String.fromCharCode(b)));
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export async function login(returnTo: string): Promise<void> {
  if (!oidcConfigured) return;
  const verifier = b64url(crypto.getRandomValues(new Uint8Array(32)));
  sessionStorage.setItem(VERIFIER_KEY, verifier);
  sessionStorage.setItem("stratt.return_to", returnTo);
  const challenge = b64url(
    new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier))),
  );
  const params = new URLSearchParams({
    client_id: CLIENT_ID!,
    redirect_uri: `${window.location.origin}/callback`,
    response_type: "code",
    scope: "openid profile email",
    code_challenge: challenge,
    code_challenge_method: "S256",
  });
  window.location.assign(`${ISSUER}/oauth/v2/authorize?${params}`);
}

// completeLogin exchanges the callback code; returns the path to restore.
export async function completeLogin(code: string): Promise<string> {
  const verifier = sessionStorage.getItem(VERIFIER_KEY);
  if (!verifier) throw new Error("no pkce verifier in session");
  const res = await fetch(`${ISSUER}/oauth/v2/token`, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      grant_type: "authorization_code",
      code,
      redirect_uri: `${window.location.origin}/callback`,
      client_id: CLIENT_ID!,
      code_verifier: verifier,
    }),
  });
  if (!res.ok) throw new Error(`token exchange failed: ${res.status}`);
  const body = (await res.json()) as {
    access_token: string;
    id_token?: string;
    expires_in: number;
  };
  const idClaims = body.id_token
    ? (JSON.parse(atob(body.id_token.split(".")[1].replace(/-/g, "+").replace(/_/g, "/"))) as Record<
        string,
        unknown
      >)
    : {};
  const tokens: Tokens = {
    accessToken: body.access_token,
    idClaims,
    expiresAt: Date.now() / 1000 + body.expires_in,
  };
  sessionStorage.setItem(KEY, JSON.stringify(tokens));
  sessionStorage.removeItem(VERIFIER_KEY);
  const returnTo = sessionStorage.getItem("stratt.return_to") ?? "/";
  sessionStorage.removeItem("stratt.return_to");
  return returnTo;
}

export function logout(): void {
  sessionStorage.removeItem(KEY);
  if (oidcConfigured) {
    window.location.assign(
      `${ISSUER}/oidc/v1/end_session?post_logout_redirect_uri=${encodeURIComponent(window.location.origin + "/")}`,
    );
  }
}

// Dev-principal fallback: mirrors the server's opt-in X-Stratt-Principal
// mode (ADR-0009) for substrate-less development. Ignored when a real token
// is present.
export function devPrincipal(): string {
  return localStorage.getItem("stratt.dev_principal") ?? "";
}
export function setDevPrincipal(id: string): void {
  if (id) localStorage.setItem("stratt.dev_principal", id);
  else localStorage.removeItem("stratt.dev_principal");
}

export function principalLabel(): string {
  const t = currentTokens();
  if (t) {
    return (
      (t.idClaims["preferred_username"] as string) ||
      (t.idClaims["email"] as string) ||
      (t.idClaims["sub"] as string) ||
      "signed in"
    );
  }
  const dev = devPrincipal();
  return dev ? `${dev} (dev header)` : "";
}
