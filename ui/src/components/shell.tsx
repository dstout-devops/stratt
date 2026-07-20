import { Link, Outlet, useMatches } from "@tanstack/react-router";
import {
  Network,
  Target,
  Play,
  ShieldAlert,
  Plug,
  Server,
  Settings,
  PanelLeftClose,
  PanelLeft,
  Sun,
  Moon,
  Monitor,
  Command as CommandIcon,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { CommandPalette } from "@/components/command-palette";
import { useUI, type Theme } from "@/store/ui";

// Seven-section IA from docs/ux/screen-catalog.md — the mental model, not the DB. Every name is a
// Named Kind grouping (charter §2); the §1.8 descent is a cross-cutting rail, not a section.
const NAV: { to: string; label: string; icon: LucideIcon }[] = [
  { to: "/graph", label: "Graph", icon: Network },
  { to: "/intents", label: "Intents", icon: Target },
  { to: "/runs", label: "Runs", icon: Play },
  { to: "/findings", label: "Findings", icon: ShieldAlert },
  { to: "/connectors", label: "Connectors", icon: Plug },
  { to: "/fleet", label: "Fleet", icon: Server },
  { to: "/admin", label: "Admin", icon: Settings },
];

const THEME_CYCLE: Record<Theme, Theme> = { system: "light", light: "dark", dark: "system" };
const THEME_ICON: Record<Theme, LucideIcon> = { system: Monitor, light: Sun, dark: Moon };

export function AppShell() {
  const collapsed = useUI((s) => s.sidebarCollapsed);
  const toggleSidebar = useUI((s) => s.toggleSidebar);
  const theme = useUI((s) => s.theme);
  const setTheme = useUI((s) => s.setTheme);
  const setPaletteOpen = useUI((s) => s.setPaletteOpen);
  const ThemeIcon = THEME_ICON[theme];

  return (
    <div className="flex h-full">
      <aside
        className={cn(
          "flex shrink-0 flex-col border-r border-border bg-card transition-[width] duration-200",
          collapsed ? "w-[52px]" : "w-[208px]",
        )}
      >
        <div className="flex h-12 items-center gap-2 px-3">
          <div className="grid size-6 place-items-center rounded bg-primary text-primary-foreground text-xs font-bold">
            S
          </div>
          {!collapsed && <span className="text-sm font-semibold tracking-tight">Stratt</span>}
        </div>
        <nav className="flex flex-1 flex-col gap-0.5 p-2">
          {NAV.map(({ to, label, icon: Icon }) => (
            <Link
              key={to}
              to={to}
              className="flex items-center gap-2.5 rounded-md px-2.5 py-1.5 text-[13px] text-subtle-foreground transition-colors hover:bg-accent hover:text-foreground [&.active]:bg-accent [&.active]:text-foreground [&.active]:font-medium"
              title={label}
            >
              <Icon className="size-4 shrink-0" />
              {!collapsed && label}
            </Link>
          ))}
        </nav>
        <div className="p-2">
          <Button variant="ghost" size="icon-sm" onClick={toggleSidebar} title="Toggle sidebar">
            {collapsed ? <PanelLeft /> : <PanelLeftClose />}
          </Button>
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-12 shrink-0 items-center gap-3 border-b border-border px-4">
          <DescentRail />
          <div className="flex-1" />
          <Button
            variant="outline"
            size="sm"
            className="gap-1.5 text-muted-foreground"
            onClick={() => setPaletteOpen(true)}
          >
            <CommandIcon className="size-3.5" />
            <kbd className="text-[11px]">⌘K</kbd>
          </Button>
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => setTheme(THEME_CYCLE[theme])}
            title={`Theme: ${theme}`}
          >
            <ThemeIcon />
          </Button>
        </header>
        <main className="min-h-0 flex-1 overflow-auto">
          <Outlet />
        </main>
      </div>
      <CommandPalette />
    </div>
  );
}

// DescentRail — the §1.8 breadcrumb, on every screen. Built from the active route's crumb data so a
// descent step is always visible and the ladder never dead-ends (ADR-0003 L2/L4).
function DescentRail() {
  const matches = useMatches();
  const crumbs = matches.map((m) => m.staticData?.crumb).filter((c): c is string => Boolean(c));
  if (crumbs.length === 0) return null;
  return (
    <nav aria-label="Descent" className="flex items-center gap-1.5 text-xs text-muted-foreground">
      {crumbs.map((c, i) => (
        <span key={i} className="flex items-center gap-1.5">
          {i > 0 && <span className="text-border">/</span>}
          <span className={cn(i === crumbs.length - 1 && "text-foreground")}>{c}</span>
        </span>
      ))}
    </nav>
  );
}
