import { cn } from "@/lib/utils";

// Shared list primitives (extracted so the section screens don't each re-roll a table/tab bar —
// the start of the "organization pass"). TableShell = a headed data table; Tabs = a segmented nav.
export function TableShell({ head, children }: { head: string[]; children: React.ReactNode }) {
  return (
    <div className="overflow-hidden rounded-lg border border-border">
      <table className="w-full text-sm">
        <thead className="bg-card text-left text-xs text-muted-foreground">
          <tr>
            {head.map((h) => (
              <th key={h} className="px-3 py-2 font-medium">
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

export function Tabs<T extends string>({
  tabs,
  value,
  onChange,
}: {
  tabs: readonly T[];
  value: T;
  onChange: (t: T) => void;
}) {
  return (
    <div className="mb-4 flex items-center gap-1">
      {tabs.map((t) => (
        <button
          key={t}
          onClick={() => onChange(t)}
          className={cn(
            "rounded-md px-3 py-1.5 text-sm capitalize transition-colors",
            value === t ? "bg-accent font-medium" : "text-muted-foreground hover:text-foreground",
          )}
        >
          {t}
        </button>
      ))}
    </div>
  );
}
