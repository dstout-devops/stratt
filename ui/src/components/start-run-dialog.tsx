import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Play } from "lucide-react";
import { Dialog, DialogContent, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { SchemaForm } from "@/components/schema-form";
import { viewsQuery, contractsQuery } from "@/lib/data";
import { contractIndex } from "@/lib/schema";
import { useStartRun } from "@/lib/mutations";
import type { Schema } from "@/api/client";

// Start a Run — the writable schema-driven authoring thesis end-to-end (ADR-0090 §5): pick a View +
// an Actuator, and the Actuator's INPUT Contract renders as a validated form (no per-Actuator React).
// Submit → POST /runs (the optimistic useStartRun). Gated like any launch (§5 — no auto-apply here).
export function StartRunDialog() {
  const [open, setOpen] = useState(false);
  const views = useQuery(viewsQuery());
  const contracts = useQuery(contractsQuery());
  const start = useStartRun();

  const actuators = useMemo(() => {
    const idx = contractIndex(contracts.data);
    return (contracts.data ?? [])
      .filter((c) => c.name.startsWith("actuators/") && c.name.endsWith(".input"))
      .map((c) => ({ name: c.name.slice("actuators/".length, -".input".length), schema: idx.get(c.name)! }));
  }, [contracts.data]);

  const [viewName, setViewName] = useState("");
  const [actuator, setActuator] = useState("");
  const selected = actuators.find((a) => a.name === actuator);

  const submit = (params: Record<string, unknown>) => {
    start.mutate(
      { viewName, actuator, params: params as unknown as Schema["StartRun"]["params"], slices: 0 },
      {
        onSuccess: () => {
          setOpen(false);
          setActuator("");
        },
      },
    );
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="sm">
          <Play /> Start Run
        </Button>
      </DialogTrigger>
      <DialogContent className="p-5">
        <DialogTitle className="text-base font-semibold">Start a Run</DialogTitle>
        <p className="mt-1 text-xs text-muted-foreground">
          Choose a View and Actuator; its input Contract renders below.
        </p>

        <div className="mt-4 grid gap-3">
          <label className="grid gap-1">
            <span className="text-sm font-medium">View</span>
            <select
              value={viewName}
              onChange={(e) => setViewName(e.target.value)}
              className="h-8 rounded-md border border-input bg-transparent px-2.5 text-[13px]"
            >
              <option value="">— select View —</option>
              {(views.data ?? []).map((v: Schema["View"]) => (
                <option key={v.name} value={v.name}>
                  {v.name}
                </option>
              ))}
            </select>
          </label>

          <label className="grid gap-1">
            <span className="text-sm font-medium">Actuator</span>
            <select
              value={actuator}
              onChange={(e) => setActuator(e.target.value)}
              className="h-8 rounded-md border border-input bg-transparent px-2.5 text-[13px]"
            >
              <option value="">— select Actuator —</option>
              {actuators.map((a) => (
                <option key={a.name} value={a.name}>
                  {a.name}
                </option>
              ))}
            </select>
          </label>
        </div>

        {selected && (
          <div className="mt-4 border-t border-border pt-4">
            <SchemaForm
              key={actuator}
              schema={selected.schema}
              submitLabel={start.isPending ? "Starting…" : "Start Run"}
              submitting={start.isPending || !viewName}
              onSubmit={submit}
            />
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
