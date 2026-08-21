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
          square for error.

          `10c` gives every toast a trailing ✕ except the working one, and
          sonner draws that distinction for us: it withholds the control on
          loading toasts, where a dismiss would suggest the job had stopped.
          The classNames below pull it out of sonner's corner-badge position
          onto the toast's own baseline, which needs `!` because sonner ships
          its geometry in an injected stylesheet at higher specificity. Sonner
          emits the ✕ first and the text after it, so ordering it last is only
          half the move: the text block has to claim the slack too, or the ✕
          stops wherever a short title ends instead of at the trailing edge.

          The other half of `10c` — errors outliving their 5s — cannot be said
          here: sonner has one duration for every kind. It is written down in
          lib/toast.tsx's toastError instead, which is the call site every
          error toast should be reaching for. */}
      <Toaster
        theme={theme}
        position="bottom-right"
        gap={10}
        duration={5000}
        closeButton
        // `10c` draws the stack open: three toasts, all readable. Sonner
        // collapses them behind the newest by default and expands on hover,
        // which hides an error under a success — the one ordering where the
        // thing you must not miss is the thing that gets covered.
        expand
        // Below `sm` the bottom tab bar is the app's only navigation, and
        // sonner parks the mobile stack 16px off the viewport floor — right on
        // top of it. Clearing the bar (and the home indicator under it) keeps
        // the four tabs both readable and tappable while a toast is up.
        mobileOffset={{
          bottom: "calc(74px + env(safe-area-inset-bottom))",
          left: "16px",
          right: "16px",
        }}
        icons={{
          success: <span aria-hidden className="size-2 flex-none rounded-full bg-pane-ok" />,
          error: <span aria-hidden className="size-2 flex-none rounded-[2px] bg-pane-error" />,
          warning: <span aria-hidden className="size-2 flex-none rounded-full bg-status-degraded" />,
          info: <span aria-hidden className="size-2 flex-none rounded-full bg-pane-info" />,
          loading: (
            <span
              aria-hidden
              className="size-3 flex-none animate-spin rounded-full border-2 border-pane-border border-t-pane-info"
            />
          ),
        }}
        // Every override below is marked important, and it has to be: sonner
        // ships its own look in a stylesheet it injects into <head> at import
        // time, unlayered, and an unlayered rule outranks anything Tailwind
        // emits into @layer utilities no matter how specific. Without the
        // marks the panel's ink toast renders as sonner's white default card.
        // The width is one of them, and marking it costs something: sonner's
        // phone rule fluid-fits the card to the gutters, and an important width
        // beats that too. The max-width hands the phone case back — 340px where
        // it fits, the 16px gutters where it does not.
        toastOptions={{
          classNames: {
            toast:
              "w-[340px]! max-w-[calc(100vw-2rem)]! items-start! gap-[11px]! rounded-[10px]! border-0! " +
              "bg-toast! px-4! py-[13px]! text-toast-text! shadow-[0_12px_32px_rgba(0,0,0,.25)]!",
            title: "text-[13px] font-semibold!",
            description: "mt-0.5 text-[12px] leading-[1.5]! text-toast-faint!",
            // Sonner leaves `[data-content]` at its intrinsic width; growing it
            // is what pushes the ✕ to the far edge, as `10c` draws it.
            content: "flex-1",
            closeButton:
              "static! order-last mt-[3px] size-auto! border-0! bg-transparent! p-0! transform-none! " +
              "text-toast-dismiss! hover:bg-transparent! hover:text-toast-text!",
            icon: "mt-[5px]",
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
