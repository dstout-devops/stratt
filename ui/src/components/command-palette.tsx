import { Command } from "cmdk";
import { VisuallyHidden } from "radix-ui";
import { useNavigate } from "@tanstack/react-router";
import {
  Network,
  Target,
  Play,
  ShieldAlert,
  Plug,
  Server,
  Settings,
  CheckCheck,
  Sun,
  Moon,
  Monitor,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { Dialog, DialogContent, DialogTitle } from "@/components/ui/dialog";
import { useUI, type Theme } from "@/store/ui";
import { useHotkey } from "@/lib/use-hotkey";

// Keyboard-first command palette (⌘K / Ctrl-K), CLI-verb parity (§1.6). Commands are plain data;
// permission-gating and richer actions (start Run, decide Gate) layer on in later slices.
const GOTO: { to: string; label: string; icon: LucideIcon }[] = [
  { to: "/graph", label: "Graph", icon: Network },
  { to: "/runs", label: "Runs", icon: Play },
  { to: "/runs/approvals", label: "Approvals", icon: CheckCheck },
  { to: "/findings", label: "Findings", icon: ShieldAlert },
  { to: "/intents", label: "Intents", icon: Target },
  { to: "/connectors", label: "Connectors", icon: Plug },
  { to: "/fleet", label: "Fleet", icon: Server },
  { to: "/admin", label: "Admin", icon: Settings },
];
const THEMES: { theme: Theme; label: string; icon: LucideIcon }[] = [
  { theme: "light", label: "Light", icon: Sun },
  { theme: "dark", label: "Dark", icon: Moon },
  { theme: "system", label: "System", icon: Monitor },
];

const ITEM =
  "flex cursor-pointer items-center gap-2.5 rounded-md px-2.5 py-2 text-sm text-subtle-foreground data-[selected=true]:bg-accent data-[selected=true]:text-foreground [&_svg]:size-4";

export function CommandPalette() {
  const open = useUI((s) => s.paletteOpen);
  const setOpen = useUI((s) => s.setPaletteOpen);
  const setTheme = useUI((s) => s.setTheme);
  const navigate = useNavigate();
  useHotkey("k", () => setOpen(!open), { mod: true, allowInEditable: true });

  const go = (to: string) => {
    setOpen(false);
    void navigate({ to });
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent className="overflow-hidden p-0">
        <VisuallyHidden.Root>
          <DialogTitle>Command palette</DialogTitle>
        </VisuallyHidden.Root>
        <Command label="Command palette" className="flex max-h-[60vh] flex-col">
          <Command.Input
            placeholder="Search commands…"
            className="h-11 w-full border-b border-border bg-transparent px-4 text-sm outline-none placeholder:text-muted-foreground"
          />
          <Command.List className="overflow-auto p-1.5">
            <Command.Empty className="px-3 py-6 text-center text-sm text-muted-foreground">
              No results.
            </Command.Empty>
            <Command.Group
              heading="Go to"
              className="[&_[cmdk-group-heading]]:px-2.5 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-xs [&_[cmdk-group-heading]]:text-muted-foreground"
            >
              {GOTO.map(({ to, label, icon: Icon }) => (
                <Command.Item
                  key={to}
                  value={`goto ${label}`}
                  onSelect={() => go(to)}
                  className={ITEM}
                >
                  <Icon /> {label}
                </Command.Item>
              ))}
            </Command.Group>
            <Command.Group
              heading="Theme"
              className="[&_[cmdk-group-heading]]:px-2.5 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-xs [&_[cmdk-group-heading]]:text-muted-foreground"
            >
              {THEMES.map(({ theme, label, icon: Icon }) => (
                <Command.Item
                  key={theme}
                  value={`theme ${label}`}
                  onSelect={() => {
                    setTheme(theme);
                    setOpen(false);
                  }}
                  className={ITEM}
                >
                  <Icon /> {label}
                </Command.Item>
              ))}
            </Command.Group>
          </Command.List>
        </Command>
      </DialogContent>
    </Dialog>
  );
}
