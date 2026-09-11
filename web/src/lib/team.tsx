// Team scope: a switcher in the sidebar footer scopes the Projects list —
// teams are context, not a place you visit often (web-ui-design.md §3).
import { createContext, useContext, useState, type ReactNode } from "react";

const STORAGE_KEY = "cypher.team";

interface TeamScope {
  teamId: string | null; // null = all teams the user can see
  setTeamId: (id: string | null) => void;
}

const TeamContext = createContext<TeamScope>({ teamId: null, setTeamId: () => {} });

export function TeamProvider({ children }: { children: ReactNode }) {
  const [teamId, setTeamIdState] = useState<string | null>(() => {
    try {
      return localStorage.getItem(STORAGE_KEY);
    } catch {
      return null;
    }
  });
  const setTeamId = (id: string | null) => {
    setTeamIdState(id);
    try {
      if (id === null) localStorage.removeItem(STORAGE_KEY);
      else localStorage.setItem(STORAGE_KEY, id);
    } catch {
      // ignore
    }
  };
  return <TeamContext.Provider value={{ teamId, setTeamId }}>{children}</TeamContext.Provider>;
}

export function useTeamScope(): TeamScope {
  return useContext(TeamContext);
}
