import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { Toaster } from "sonner";
import { router } from "@/router";
import { queryClient } from "@/lib/query";
import "@/store/ui"; // rehydrates + applies the persisted theme before first paint
import "./index.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
      <Toaster position="bottom-right" richColors expand />
    </QueryClientProvider>
  </StrictMode>,
);
