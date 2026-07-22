import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { Check, X } from "lucide-react";
import { gatesQuery } from "@/lib/data";
import { useDecideGate } from "@/lib/mutations";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import { relTime } from "@/lib/format";
import { ErrorLine, EmptyState } from "@/components/feedback";
import type { Schema } from "@/api/client";

// The approval inbox lives UNDER Runs (vocabulary-linter: Gate is a Workflow feature, not a
// top-level Named-Kind section). Decisions are optimistic — the card leaves the inbox on click,
// the network settles behind it. Agents use the identical POST /gates/{id}/decision (§1.6).
export function ApprovalsInbox() {
  const { data, isPending, error } = useQuery(gatesQuery("pending"));
  const gates = data ?? [];
  return (
    <div className="p-6">
      <div className="mb-4 flex items-center gap-3">
        <Link to="/runs" className="text-sm text-muted-foreground hover:text-foreground">
          Runs
        </Link>
        <span className="text-border">/</span>
        <h1 className="text-lg font-semibold">Approvals</h1>
        <Badge variant="outline">{gates.length} pending</Badge>
      </div>
      {error && <ErrorLine error={error} />}
      {isPending ? (
        <Skeleton className="h-32 w-full" />
      ) : gates.length === 0 ? (
        <EmptyState label="No Gates awaiting a decision." />
      ) : (
        <div className="grid gap-3">
          {gates.map((g) => (
            <GateCard key={g.id} gate={g} />
          ))}
        </div>
      )}
    </div>
  );
}

function GateCard({ gate }: { gate: Schema["Gate"] }) {
  const decide = useDecideGate();
  return (
    <div className="flex items-center gap-4 rounded-lg border border-border bg-card p-4">
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium">{gate.step}</span>
          <Link
            to="/workflow-runs/$id"
            params={{ id: gate.workflowRunId }}
            className="text-xs text-primary hover:underline"
          >
            workflow-run ↗
          </Link>
        </div>
        <div className="mt-1 text-xs text-muted-foreground">
          raised {relTime(gate.createdAt)}
          {gate.approvers.principals && gate.approvers.principals.length > 0 && (
            <> · approvers: {gate.approvers.principals.join(", ")}</>
          )}
        </div>
      </div>
      <div className="flex shrink-0 gap-2">
        <Button
          size="sm"
          variant="secondary"
          disabled={decide.isPending}
          onClick={() => decide.mutate({ id: gate.id, approve: false })}
        >
          <X /> Deny
        </Button>
        <Button
          size="sm"
          disabled={decide.isPending}
          onClick={() => decide.mutate({ id: gate.id, approve: true })}
        >
          <Check /> Approve
        </Button>
      </div>
    </div>
  );
}
