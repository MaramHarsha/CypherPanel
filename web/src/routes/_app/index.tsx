import { createFileRoute, redirect } from "@tanstack/react-router";

// The landing page is Projects (ui-principles §4).
export const Route = createFileRoute("/_app/")({
  beforeLoad: () => {
    throw redirect({ to: "/projects" });
  },
});
