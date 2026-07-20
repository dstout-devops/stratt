import { QueryClient } from "@tanstack/react-query";

// 30s stale default → navigations within the window are cache hits, zero network (the base of the
// "feels instant" story). Hot lists override to 15s in their queryOptions.
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 30_000, retry: 1, refetchOnWindowFocus: false },
  },
});
