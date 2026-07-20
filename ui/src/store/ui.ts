import { create } from "zustand";
import { persist } from "zustand/middleware";
import { getDevPrincipal, setDevPrincipal } from "@/lib/session";

// UI-only client state (ADR-0090 §8). Server data NEVER lives here — that's TanStack Query. Only
// theme, chrome, palette, and the dev identity. Theme + sidebar persist; palette is transient.
export type Theme = "light" | "dark" | "system";

export function applyTheme(theme: Theme) {
  const root = document.documentElement;
  if (theme === "system") root.removeAttribute("data-theme");
  else root.setAttribute("data-theme", theme);
}

interface UIState {
  theme: Theme;
  setTheme: (t: Theme) => void;
  sidebarCollapsed: boolean;
  toggleSidebar: () => void;
  paletteOpen: boolean;
  setPaletteOpen: (b: boolean) => void;
  devPrincipal: string;
  changeDevPrincipal: (id: string) => void;
}

export const useUI = create<UIState>()(
  persist(
    (set) => ({
      theme: "system",
      setTheme: (theme) => {
        applyTheme(theme);
        set({ theme });
      },
      sidebarCollapsed: false,
      toggleSidebar: () => set((s) => ({ sidebarCollapsed: !s.sidebarCollapsed })),
      paletteOpen: false,
      setPaletteOpen: (paletteOpen) => set({ paletteOpen }),
      devPrincipal: getDevPrincipal(),
      changeDevPrincipal: (id) => {
        setDevPrincipal(id);
        set({ devPrincipal: id });
      },
    }),
    {
      name: "stratt.ui",
      partialize: (s) => ({ theme: s.theme, sidebarCollapsed: s.sidebarCollapsed }),
      onRehydrateStorage: () => (state) => {
        if (state) applyTheme(state.theme);
      },
    },
  ),
);
