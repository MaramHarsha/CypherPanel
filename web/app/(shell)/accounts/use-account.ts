"use client";

import { useQuery } from "@tanstack/react-query";
import { listAccounts, type AccountInfo } from "@/lib/api";

// Every per-account page (hub + each feature page) needs the account's own
// fields (username, primary_domain, status, ...) alongside its id from the
// route. There's no single-account GET endpoint, so this shares the same
// `["accounts"]` list query/cache the accounts list page already populates
// rather than adding a new fetch per page.
export function useAccount(id: string): { account: AccountInfo | undefined; isLoading: boolean } {
  const { data, isLoading } = useQuery({ queryKey: ["accounts"], queryFn: listAccounts });
  return { account: data?.find((a) => a.id === id), isLoading };
}
