import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

/** cn merges Tailwind classes with conflict resolution (the shadcn canonical util). */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
