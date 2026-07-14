"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import {
  Boxes,
  LayoutDashboard,
  LogOut,
  Package,
  Server,
  Shield,
  Users,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import { ThemeToggle } from "@/components/theme-toggle";
import { cn } from "@/lib/utils";
import { hasSession, logout } from "@/lib/api";

const nav = [
  { href: "/dashboard", label: "Dashboard", icon: LayoutDashboard },
  { href: "/servers", label: "Servers", icon: Server },
  { href: "/packages", label: "Packages", icon: Package, disabled: true },
  { href: "/accounts", label: "Accounts", icon: Users, disabled: true },
] as const;

export default function ShellLayout({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const pathname = usePathname();
  const [ready, setReady] = useState(false);

  // Client-side session guard: no session → login. (Server-side enforcement
  // is the API itself; this only avoids rendering a dead shell.)
  useEffect(() => {
    if (!hasSession()) {
      router.replace("/login");
    } else {
      setReady(true);
    }
  }, [router]);

  if (!ready) return null;

  return (
    <div className="flex min-h-screen">
      <aside className="hidden w-60 shrink-0 flex-col border-r bg-sidebar text-sidebar-foreground md:flex">
        <div className="flex h-14 items-center gap-2 px-4 font-semibold">
          <Shield className="h-5 w-5" />
          CypherPanel
        </div>
        <Separator />
        <nav className="flex flex-1 flex-col gap-1 p-2">
          {nav.map((item) => {
            const active = pathname.startsWith(item.href);
            const Icon = item.icon;
            if ("disabled" in item && item.disabled) {
              return (
                <span
                  key={item.href}
                  aria-disabled
                  className="flex items-center gap-3 rounded-md px-3 py-2 text-sm text-muted-foreground/60"
                  title="Coming soon"
                >
                  <Icon className="h-4 w-4" />
                  {item.label}
                </span>
              );
            }
            return (
              <Link
                key={item.href}
                href={item.href}
                className={cn(
                  "flex items-center gap-3 rounded-md px-3 py-2 text-sm transition-colors",
                  active
                    ? "bg-sidebar-accent font-medium text-sidebar-accent-foreground"
                    : "hover:bg-sidebar-accent/60",
                )}
              >
                <Icon className="h-4 w-4" />
                {item.label}
              </Link>
            );
          })}
        </nav>
        <Separator />
        <div className="p-2">
          <Button
            variant="ghost"
            className="w-full justify-start gap-3"
            onClick={async () => {
              await logout();
              router.replace("/login");
            }}
          >
            <LogOut className="h-4 w-4" />
            Sign out
          </Button>
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 items-center justify-between border-b px-4">
          <div className="flex items-center gap-2 font-medium md:hidden">
            <Boxes className="h-5 w-5" />
            CypherPanel
          </div>
          <div className="ml-auto">
            <ThemeToggle />
          </div>
        </header>
        <main className="flex-1 p-6">{children}</main>
      </div>
    </div>
  );
}
