import { useMutation } from "@tanstack/react-query";
import type { QueryKey } from "@tanstack/react-query";
import { toast } from "sonner";
import { api, unwrap, type Schema } from "@/api/client";
import { queryClient } from "@/lib/query";

// The optimistic-mutation template (ADR-0090 §2): onMutate cancel→snapshot→setQueryData, onError
// restore, onSettled invalidate. The Gate decision moves the card off the inbox synchronously — the
// approver never waits on the network to see it go.
export function useDecideGate() {
  return useMutation({
    mutationFn: async (v: { id: string; approve: boolean; note?: string }) =>
      unwrap(
        await api.POST("/gates/{id}/decision", {
          params: { path: { id: v.id } },
          body: { approve: v.approve, note: v.note },
        }),
      ),
    onMutate: async (v) => {
      await queryClient.cancelQueries({ queryKey: ["gates"] });
      const snapshot = queryClient.getQueriesData<Schema["Gate"][]>({ queryKey: ["gates"] });
      for (const [key, list] of snapshot) {
        if (Array.isArray(list)) {
          queryClient.setQueryData(
            key,
            list.filter((g) => g.id !== v.id),
          );
        }
      }
      return { snapshot };
    },
    onError: (_e, _v, ctx) => {
      ctx?.snapshot.forEach(([key, data]: [QueryKey, unknown]) =>
        queryClient.setQueryData(key, data),
      );
      toast.error("Gate decision failed");
    },
    onSuccess: (_d, v) => toast.success(v.approve ? "Gate approved" : "Gate denied"),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["gates"] }),
  });
}

// Start a Run from a (schema-rendered) params form. No optimistic cache write — the Run id is
// server-assigned — but the runs list refetches on success so the new Run appears immediately.
export function useStartRun() {
  return useMutation({
    mutationFn: async (body: Schema["StartRun"]) => unwrap(await api.POST("/runs", { body })),
    onSuccess: (run) => {
      queryClient.invalidateQueries({ queryKey: ["runs"] });
      toast.success(`Run ${run.id} started`);
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Run failed to start"),
  });
}
