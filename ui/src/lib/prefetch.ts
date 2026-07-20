import { useRef } from "react";
import type { FetchQueryOptions, QueryKey } from "@tanstack/react-query";
import { queryClient } from "@/lib/query";

// Hover-prefetch with a 100ms debounce (gauntlet's pattern): sweeping the pointer across a list
// doesn't queue N requests, but by the time a click lands the detail is already cached — the
// navigation is a cache hit. Wire on list rows: {...useHoverPrefetch(runQuery(id))}.
export function useHoverPrefetch<TData, TError, TQueryKey extends QueryKey>(
  options: FetchQueryOptions<TData, TError, TData, TQueryKey>,
) {
  const timer = useRef<number | undefined>(undefined);
  return {
    onMouseEnter: () => {
      window.clearTimeout(timer.current);
      timer.current = window.setTimeout(() => void queryClient.prefetchQuery(options), 100);
    },
    onMouseLeave: () => window.clearTimeout(timer.current),
  };
}
