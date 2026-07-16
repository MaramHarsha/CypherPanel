import {
  LayoutDashboard,
  Package,
  ScrollText,
  Server,
  Store,
  Users,
  type LucideIcon,
} from "lucide-react";

export type NavItem = {
  href: string;
  label: string;
  icon: LucideIcon;
  adminOnly: boolean;
};

export type NavGroup = {
  label: string;
  items: NavItem[];
};

// adminOnly items are hidden from resellers (fleet infrastructure + managing
// other resellers are root-admin concerns; the API enforces this too).
// Shared by the sidebar (layout.tsx) and the command palette so the two
// never drift apart.
export const navGroups: NavGroup[] = [
  {
    label: "Overview",
    items: [{ href: "/dashboard", label: "Dashboard", icon: LayoutDashboard, adminOnly: false }],
  },
  {
    label: "Infrastructure",
    items: [
      { href: "/servers", label: "Servers", icon: Server, adminOnly: true },
      { href: "/audit", label: "Audit log", icon: ScrollText, adminOnly: true },
    ],
  },
  {
    label: "Hosting",
    items: [
      { href: "/packages", label: "Packages", icon: Package, adminOnly: false },
      { href: "/accounts", label: "Accounts", icon: Users, adminOnly: false },
      { href: "/resellers", label: "Resellers", icon: Store, adminOnly: true },
    ],
  },
];

export function visibleNavGroups(isAdmin: boolean): NavGroup[] {
  return navGroups
    .map((g) => ({ ...g, items: g.items.filter((i) => isAdmin || !i.adminOnly) }))
    .filter((g) => g.items.length > 0);
}
