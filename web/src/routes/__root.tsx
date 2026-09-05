import { createRootRoute, Outlet } from "@tanstack/react-router";
import { type CSSProperties } from "react";
import { Toaster } from "sonner";
import { NotFoundPage } from "@/components/error-page";
import { TooltipProvider } from "@/components/ui/tooltip";

function Root() {
  return (
    <TooltipProvider>
      <Outlet />
      {/* Canvas `10c`. The stack lives here; the cards do not. Every toast in
          the panel is drawn by lib/toast.tsx — ink in both themes (see --toast
          in globals.css), a dot instead of a glyph with its shape carrying the
          same meaning as its colour, and the one thing a Toaster cannot say:
          that an error outlives the five seconds a success gets. So this
          Toaster is unstyled on purpose. A raw sonner call from anywhere else
          would render as bare text, which is the point — it is not meant to
          exist, and lib/toast.tsx is the only import of sonner besides this. */}
      <Toaster
        position="bottom-right"
        gap={10}
        // `10c` draws the stack open: three toasts, all readable. Sonner
        // collapses them behind the newest by default and expands on hover,
        // which hides an error under a success — the one ordering where the
        // thing you must not miss is the thing that gets covered.
        expand
        // The card is 360px (`10c`); sonner's column defaults to 356 and
        // anchors each toast to its right edge, so the column has to be told.
        style={{ "--width": "360px" } as CSSProperties}
        // Below `sm` the bottom tab bar is the app's only navigation, and
        // sonner parks the mobile stack 16px off the viewport floor — right on
        // top of it. Clearing the bar (and the home indicator under it) keeps
        // the four tabs both readable and tappable while a toast is up.
        mobileOffset={{
          bottom: "calc(74px + env(safe-area-inset-bottom))",
          left: "16px",
          right: "16px",
        }}
        toastOptions={{ unstyled: true }}
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
