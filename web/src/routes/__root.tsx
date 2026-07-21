import { createRootRoute, Outlet } from "@tanstack/react-router";
import { Toaster } from "sonner";
import { TooltipProvider } from "@/components/ui/tooltip";
import { useTheme } from "@/lib/theme";

function Root() {
  const theme = useTheme();
  return (
    <TooltipProvider>
      <Outlet />
      <Toaster
        theme={theme}
        position="bottom-right"
        toastOptions={{
          style: {
            background: "var(--overlay)",
            border: "1px solid var(--border)",
            color: "var(--text)",
          },
        }}
      />
    </TooltipProvider>
  );
}

export const Route = createRootRoute({ component: Root });
