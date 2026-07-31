"use client";

import { useMemo } from "react";
import { useRouter } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { useTheme } from "next-themes";
import { Moon, Plus, Server, Sun, Users } from "lucide-react";
import {
  Command,
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command";
import { visibleNavGroups } from "@/lib/nav";
import { listAccounts, listServers } from "@/lib/api";

// Global fuzzy-search over every page, account, and server — the escape
// hatch for a deep feature set (Design.md §5 / plan.md §5). Opens on
// Ctrl/Cmd+K (wired in the shell layout) or the header search button.
export function CommandPalette({
  open,
  onOpenChange,
  isAdmin,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  isAdmin: boolean;
}) {
  const router = useRouter();
  const { theme, setTheme } = useTheme();

  // Reuses the same queryKeys the dashboard/accounts/servers pages already
  // populate — opening the palette is nearly always a cache hit, not a
  // fresh fetch.
  const { data: accounts } = useQuery({ queryKey: ["accounts"], queryFn: listAccounts, enabled: open });
  const { data: servers } = useQuery({
    queryKey: ["servers"],
    // Wrapped, not passed bare: listServers takes an optional region, and
    // TanStack would hand it the query context as that argument.
    queryFn: () => listServers(),
    enabled: open && isAdmin,
  });

  const navGroups = useMemo(() => visibleNavGroups(isAdmin), [isAdmin]);

  const go = (href: string) => {
    onOpenChange(false);
    router.push(href);
  };

  return (
    <CommandDialog
      open={open}
      onOpenChange={onOpenChange}
      title="Command palette"
      description="Jump to any page, account, or server"
    >
      <Command>
        <CommandInput placeholder="Search pages, accounts, servers…" />
        <CommandList>
          <CommandEmpty>No results found.</CommandEmpty>

          <CommandGroup heading="Quick actions">
            <CommandItem value="new account create hosting" onSelect={() => go("/accounts?new=1")}>
              <Plus /> New account
            </CommandItem>
            {isAdmin && (
              <CommandItem value="new package create plan" onSelect={() => go("/packages?new=1")}>
                <Plus /> New package
              </CommandItem>
            )}
            <CommandItem
              value="toggle theme dark light appearance"
              onSelect={() => {
                setTheme(theme === "dark" ? "light" : "dark");
                onOpenChange(false);
              }}
            >
              {theme === "dark" ? <Sun /> : <Moon />}
              Toggle theme
            </CommandItem>
          </CommandGroup>

          <CommandSeparator />

          <CommandGroup heading="Navigate">
            {navGroups.flatMap((g) =>
              g.items.map((item) => (
                <CommandItem key={item.href} value={`${g.label} ${item.label}`} onSelect={() => go(item.href)}>
                  <item.icon />
                  {item.label}
                </CommandItem>
              )),
            )}
          </CommandGroup>

          {accounts && accounts.length > 0 && (
            <>
              <CommandSeparator />
              <CommandGroup heading="Accounts">
                {accounts.slice(0, 50).map((a) => (
                  <CommandItem
                    key={a.id}
                    value={`account ${a.username} ${a.primary_domain}`}
                    onSelect={() => go(`/accounts/${a.id}`)}
                  >
                    <Users />
                    <span className="truncate">{a.username}</span>
                    <span className="ml-1 truncate font-mono text-xs text-muted-foreground">
                      {a.primary_domain}
                    </span>
                  </CommandItem>
                ))}
              </CommandGroup>
            </>
          )}

          {isAdmin && servers && servers.length > 0 && (
            <>
              <CommandSeparator />
              <CommandGroup heading="Servers">
                {servers.map((s) => (
                  <CommandItem
                    key={s.id}
                    value={`server ${s.name} ${s.ip_address}`}
                    onSelect={() => go(`/servers/${s.id}`)}
                  >
                    <Server />
                    <span className="truncate">{s.name}</span>
                    <span className="ml-1 truncate font-mono text-xs text-muted-foreground">
                      {s.ip_address}
                    </span>
                  </CommandItem>
                ))}
              </CommandGroup>
            </>
          )}
        </CommandList>
      </Command>
    </CommandDialog>
  );
}
