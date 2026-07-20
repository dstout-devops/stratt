import { Construction } from "lucide-react";

// Sections beyond the slice-1 descent spine (Intents, Connectors, Fleet, Admin) are scaffolded in
// the IA but land in later ADR-0090 slices. Honest placeholder — never a dead nav item.
export function Placeholder({ title, slice }: { title: string; slice: string }) {
  return (
    <div className="grid h-full place-items-center p-8">
      <div className="flex max-w-md flex-col items-center gap-3 text-center">
        <Construction className="size-8 text-muted-foreground" />
        <h1 className="text-lg font-semibold">{title}</h1>
        <p className="text-sm text-muted-foreground">
          This section is scaffolded in the information architecture and ships in {slice}. The
          descent spine (Graph → Runs → Findings) is the current slice.
        </p>
      </div>
    </div>
  );
}
