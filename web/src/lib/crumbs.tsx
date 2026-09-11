// Breadcrumb context: pages declare their trail; the app header renders it —
// team / project / environment / resource, always (ui-principles §4).
import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import { type Crumb } from "@/components/breadcrumbs";

interface CrumbsState {
  crumbs: Crumb[];
  set: (crumbs: Crumb[]) => void;
}

const CrumbsContext = createContext<CrumbsState>({ crumbs: [], set: () => {} });

export function CrumbsProvider({ children }: { children: ReactNode }) {
  const [crumbs, set] = useState<Crumb[]>([]);
  return <CrumbsContext.Provider value={{ crumbs, set }}>{children}</CrumbsContext.Provider>;
}

export function useCrumbsValue(): Crumb[] {
  return useContext(CrumbsContext).crumbs;
}

/** Declare this page's breadcrumb trail. */
export function useCrumbs(crumbs: Crumb[]): void {
  const { set } = useContext(CrumbsContext);
  const key = JSON.stringify(crumbs);
  useEffect(() => {
    set(JSON.parse(key) as Crumb[]);
    return () => set([]);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
}
