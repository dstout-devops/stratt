import createClient, { type Middleware } from "openapi-fetch";
import type { paths, components } from "@/api/schema";
import { authHeader } from "@/lib/session";

/** Schema is the generated view-model namespace — `Schema["Run"]`, `Schema["Finding"]`, … */
export type Schema = components["schemas"];

// One shared, typed client over /api/v1 (dev-proxied to strattd :8080). openapi-fetch gives
// end-to-end typed PATHS + params + responses — closing the old hand-written client's untyped-path
// gap (ADR-0090 §1). Auth rides a per-request middleware so identity switches need no rebuild.
const auth: Middleware = {
  onRequest({ request }) {
    for (const [k, v] of Object.entries(authHeader())) request.headers.set(k, v);
    return request;
  },
};

export const api = createClient<paths>({ baseUrl: "/api/v1" });
api.use(auth);

/** unwrap turns openapi-fetch's {data,error} into a throwing queryFn result, surfacing the API's
 * own error message verbatim (§1.8 — never swallow the diagnosis). */
export function unwrap<T>(res: { data?: T; error?: unknown }): T {
  if (res.error !== undefined) {
    const msg =
      res.error && typeof res.error === "object" && "message" in res.error
        ? String((res.error as { message?: unknown }).message)
        : "request failed";
    throw new Error(msg);
  }
  return res.data as T;
}
