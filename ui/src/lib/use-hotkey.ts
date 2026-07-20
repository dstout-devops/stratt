import { useEffect } from "react";

// Single global keyboard shortcut (gauntlet's use-keyboard-shortcut, trimmed). Guards against
// firing while typing in an editable element unless allowInEditable.
export function useHotkey(
  key: string,
  handler: () => void,
  opts: { mod?: boolean; allowInEditable?: boolean } = {},
) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() !== key.toLowerCase()) return;
      if (opts.mod && !(e.metaKey || e.ctrlKey)) return;
      const el = e.target as HTMLElement | null;
      const editable =
        el && (el.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(el.tagName));
      if (editable && !opts.allowInEditable) return;
      e.preventDefault();
      handler();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [key, handler, opts.mod, opts.allowInEditable]);
}
