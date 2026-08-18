import { createRootRoute, Outlet } from "@tanstack/react-router";
import { Toaster } from "sonner";
import { NotFoundPage } from "@/components/error-page";
import { TooltipProvider } from "@/components/ui/tooltip";
import { useTheme } from "@/lib/theme";

function Root() {
  const theme = useTheme();
  return (
    <TooltipProvider>
      <Outlet />
      {/* Canvas `10c`. Styled at the Toaster rather than per call so the ~50
          existing toast.success/error sites inherit the design instead of
          needing a rewrite each — and so a new one cannot be styled wrong.
          Ink in both themes (see --toast in globals.css), dot instead of a
          glyph, and shape carrying the same meaning as colour: round for ok,
          square for error. Success auto-dismisses at 5s; errors are given
          Infinity here because an error toast without a next step is a dead
          end, and it should stay until the operator has read it. */}
      <Toaster
        theme={theme}
        position="bottom-right"
        gap={10}
        duration={5000}
        // `10c` draws the stack open: three toasts, all readable. Sonner
        // collapses them behind the newest by default and expands on hover,
        // which hides an error under a success — the one ordering where the
        // thing you must not miss is the thing that gets covered.
        expand
        icons={{
          success: <span aria-hidden className="size-2 flex-none rounded-full bg-[#4ac26b]" />,
          error: <span aria-hidden className="size-2 flex-none rounded-[2px] bg-[#ff6a5e]" />,
          warning: <span aria-hidden className="size-2 flex-none rounded-full bg-status-degraded" />,
          info: <span aria-hidden className="size-2 flex-none rounded-full bg-[#58a6ff]" />,
          loading: (
            <span
              aria-hidden
              className="size-3 flex-none animate-spin rounded-full border-2 border-pane-border border-t-[#58a6ff]"
            />
          ),
        }}
        toastOptions={{
          classNames: {
            toast:
              "w-[340px] items-start gap-[11px] rounded-[10px] border-0 bg-toast px-4 py-[13px] text-toast-text shadow-[0_12px_32px_rgba(0,0,0,.25)]",
            title: "text-[13px] font-semibold",
            description: "mt-0.5 text-[12px] leading-[1.5] text-toast-faint",
            actionButton: "text-[12px] font-semibold text-toast-text",
            cancelButton: "text-[12px] text-toast-faint",
            closeButton: "border-0 bg-transparent text-toast-dismiss hover:text-toast-text",
            icon: "mt-[5px]",
            error: "[&>[data-close-button]]:opacity-100",
          },
        }}
      />
    </TooltipProvider>
  );
}

// A URL matching no route at all resolves against the *root* route, so the
// 404 has to be declared here — the router's `defaultNotFoundComponent` only
// covers `notFound()` thrown from a loader, and without this an unknown path
// fell through to the router's built-in bare "Not Found" text.
export const Route = createRootRoute({
  component: Root,
  notFoundComponent: () => <NotFoundPage />,
});
