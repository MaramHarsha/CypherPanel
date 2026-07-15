"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { Ban, CirclePlay, FolderTree, Lock, LockOpen, Plus, Trash2, Users } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { PageHeader } from "@/components/page-header";
import {
  accountAction,
  ApiError,
  createAccount,
  issueSSL,
  listAccounts,
  listPackages,
  listServers,
  type AccountInfo,
} from "@/lib/api";
import { PHPSettingsDialog } from "./php-settings-dialog";
import { DatabasesDialog } from "./databases-dialog";
import { FTPDialog } from "./ftp-dialog";
import { DNSDialog } from "./dns-dialog";
import { CronDialog } from "./cron-dialog";
import { MailDialog } from "./mail-dialog";

const statusVariant: Record<
  string,
  "default" | "secondary" | "destructive" | "outline" | "success" | "warning"
> = {
  active: "success",
  provisioning: "warning",
  suspended: "outline",
  terminating: "warning",
  failed: "destructive",
};

function CreateAccountDialog() {
  const qc = useQueryClient();
  const { data: servers } = useQuery({ queryKey: ["servers"], queryFn: listServers });
  const { data: packages } = useQuery({ queryKey: ["packages"], queryFn: listPackages });

  const [open, setOpen] = useState(false);
  const [form, setForm] = useState({
    username: "",
    email: "",
    password: "",
    domain: "",
    server_id: "",
    package_id: "",
  });
  const [error, setError] = useState<string | null>(null);

  const create = useMutation({
    mutationFn: () => createAccount(form),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["accounts"] });
      setOpen(false);
      setForm({ username: "", email: "", password: "", domain: "", server_id: "", package_id: "" });
      setError(null);
    },
    onError: (e) => setError(e instanceof ApiError ? e.message : "Failed to create account"),
  });

  const ready =
    form.username && form.email && form.password.length >= 12 && form.domain &&
    form.server_id && form.package_id;

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={<Button />}>
        <Plus className="h-4 w-4" /> New account
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>New hosting account</DialogTitle>
          <DialogDescription>
            Provisions a Linux user on the selected server via the agent pipeline.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid grid-cols-2 gap-3">
            <div className="grid gap-1.5">
              <Label htmlFor="username">Username</Label>
              <Input
                id="username"
                placeholder="alice"
                value={form.username}
                onChange={(e) => setForm((f) => ({ ...f, username: e.target.value }))}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="domain">Primary domain</Label>
              <Input
                id="domain"
                placeholder="alice.example.com"
                value={form.domain}
                onChange={(e) => setForm((f) => ({ ...f, domain: e.target.value }))}
              />
            </div>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="email">Email</Label>
            <Input
              id="email"
              type="email"
              value={form.email}
              onChange={(e) => setForm((f) => ({ ...f, email: e.target.value }))}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="password">Password (12+ chars)</Label>
            <Input
              id="password"
              type="password"
              value={form.password}
              onChange={(e) => setForm((f) => ({ ...f, password: e.target.value }))}
            />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div className="grid gap-1.5">
              <Label>Server</Label>
              <Select
                value={form.server_id}
                onValueChange={(v) => setForm((f) => ({ ...f, server_id: v ?? "" }))}
              >
                <SelectTrigger>
                  <SelectValue placeholder="Select server" />
                </SelectTrigger>
                <SelectContent>
                  {(servers ?? [])
                    .filter((s) => s.agent_status === "online")
                    .map((s) => (
                      <SelectItem key={s.id} value={s.id!}>
                        {s.name}
                      </SelectItem>
                    ))}
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label>Package</Label>
              <Select
                value={form.package_id}
                onValueChange={(v) => setForm((f) => ({ ...f, package_id: v ?? "" }))}
              >
                <SelectTrigger>
                  <SelectValue placeholder="Select package" />
                </SelectTrigger>
                <SelectContent>
                  {(packages ?? []).map((p) => (
                    <SelectItem key={p.id} value={p.id!}>
                      {p.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          {error && <p className="text-sm text-destructive">{error}</p>}
        </div>
        <DialogFooter>
          <Button onClick={() => create.mutate()} disabled={!ready || create.isPending}>
            {create.isPending ? "Provisioning…" : "Create account"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function TerminateButton({ account }: { account: AccountInfo }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [confirm, setConfirm] = useState("");
  const terminate = useMutation({
    mutationFn: () => accountAction(account.id!, "terminate"),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["accounts"] });
      setOpen(false);
      setConfirm("");
    },
  });

  return (
    <AlertDialog open={open} onOpenChange={setOpen}>
      <Button
        variant="ghost"
        size="icon"
        aria-label="Terminate account"
        onClick={() => setOpen(true)}
      >
        <Trash2 className="h-4 w-4 text-destructive" />
      </Button>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Terminate {account.username}?</AlertDialogTitle>
          <AlertDialogDescription>
            This permanently deletes the account and its Linux user on{" "}
            {account.server_name}. This cannot be undone. Type the username{" "}
            <span className="font-mono font-semibold">{account.username}</span> to confirm.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <Input
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
          placeholder={account.username}
          autoComplete="off"
        />
        <AlertDialogFooter>
          <AlertDialogCancel onClick={() => setConfirm("")}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={confirm !== account.username || terminate.isPending}
            onClick={(e) => {
              e.preventDefault();
              terminate.mutate();
            }}
          >
            {terminate.isPending ? "Terminating…" : "Terminate"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

export default function AccountsPage() {
  const qc = useQueryClient();
  const { data, isLoading } = useQuery({
    queryKey: ["accounts"],
    queryFn: listAccounts,
    refetchInterval: 5000, // reflect provisioning → active transitions promptly
  });

  const toggle = useMutation({
    mutationFn: ({ id, action }: { id: string; action: "suspend" | "unsuspend" }) =>
      accountAction(id, action),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["accounts"] }),
  });

  const ssl = useMutation({
    mutationFn: (id: string) => issueSSL(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["accounts"] }),
  });

  return (
    <div>
      <PageHeader
        title="Accounts"
        description="Hosting accounts across the fleet, with live provisioning status."
      >
        <CreateAccountDialog />
      </PageHeader>

      <Card>
        <CardContent className="pt-6">
          {isLoading ? (
            <div className="space-y-2">
              {[0, 1, 2].map((i) => (
                <Skeleton key={i} className="h-10 w-full" />
              ))}
            </div>
          ) : data && data.length > 0 ? (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b text-left text-muted-foreground">
                    <th className="py-2 pr-4 font-medium">Username</th>
                    <th className="py-2 pr-4 font-medium">Domain</th>
                    <th className="py-2 pr-4 font-medium">Server</th>
                    <th className="py-2 pr-4 font-medium">Package</th>
                    <th className="py-2 pr-4 font-medium">PHP</th>
                    <th className="py-2 pr-4 font-medium">SSL</th>
                    <th className="py-2 pr-4 font-medium">Status</th>
                    <th className="py-2 pr-4 font-medium text-right">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {data.map((a) => (
                    <tr
                      key={a.id}
                      className="border-b transition-colors last:border-0 hover:bg-muted/50"
                    >
                      <td className="py-2.5 pr-4 font-medium">{a.username}</td>
                      <td className="py-2.5 pr-4 font-mono text-xs">{a.primary_domain}</td>
                      <td className="py-2.5 pr-4">{a.server_name}</td>
                      <td className="py-2.5 pr-4">{a.package_name}</td>
                      <td className="py-2.5 pr-4 font-mono text-xs">{a.php_version || "—"}</td>
                      <td className="py-2.5 pr-4">
                        {a.ssl_status === "active" ? (
                          <span
                            className="inline-flex items-center gap-1 font-medium text-success"
                            title={a.ssl_expires_at ? `Expires ${new Date(a.ssl_expires_at).toLocaleDateString()}` : "Active"}
                          >
                            <Lock className="h-3.5 w-3.5" /> HTTPS
                          </span>
                        ) : a.ssl_status === "issuing" ? (
                          <span className="text-xs text-muted-foreground">issuing…</span>
                        ) : (
                          <Button
                            variant="ghost"
                            size="sm"
                            className="h-7 gap-1 px-2 text-xs"
                            disabled={a.status !== "active" || ssl.isPending}
                            onClick={() => ssl.mutate(a.id!)}
                          >
                            <LockOpen className="h-3.5 w-3.5" />
                            {a.ssl_status === "failed" ? "Retry" : "Issue"}
                          </Button>
                        )}
                      </td>
                      <td className="py-2.5 pr-4">
                        <Badge variant={statusVariant[a.status ?? ""] ?? "secondary"}>
                          {a.status}
                        </Badge>
                      </td>
                      <td className="py-2.5 pr-4">
                        <div className="flex justify-end gap-1">
                          {a.status === "active" && (
                            <Link
                              href={`/accounts/${a.id}/files`}
                              aria-label="File manager"
                              title="File manager"
                              className="inline-flex h-8 w-8 items-center justify-center rounded-lg text-foreground transition-colors hover:bg-muted"
                            >
                              <FolderTree className="h-4 w-4" />
                            </Link>
                          )}
                          <DatabasesDialog account={a} />
                          <FTPDialog account={a} />
                          <DNSDialog account={a} />
                          <MailDialog account={a} />
                          <CronDialog account={a} />
                          <PHPSettingsDialog account={a} />
                          {a.status === "suspended" ? (
                            <Button
                              variant="ghost"
                              size="icon"
                              aria-label="Unsuspend"
                              onClick={() => toggle.mutate({ id: a.id!, action: "unsuspend" })}
                            >
                              <CirclePlay className="h-4 w-4 text-success" />
                            </Button>
                          ) : (
                            <Button
                              variant="ghost"
                              size="icon"
                              aria-label="Suspend"
                              disabled={a.status !== "active"}
                              onClick={() => toggle.mutate({ id: a.id!, action: "suspend" })}
                            >
                              <Ban className="h-4 w-4" />
                            </Button>
                          )}
                          <TerminateButton account={a} />
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <div className="flex flex-col items-center py-12 text-center">
              <Users className="mb-2 h-8 w-8 text-muted-foreground" />
              <CardTitle className="text-base">No accounts yet</CardTitle>
              <CardDescription>
                Create a package and an online server, then provision your first account.
              </CardDescription>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
