"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  ArrowLeft,
  Clock,
  Database,
  FolderTree,
  FolderUp,
  Globe,
  Lock,
  LockOpen,
  Mail,
  Settings2,
  SquareTerminal,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { PageHeader } from "@/components/page-header";
import { issueSSL } from "@/lib/api";
import { useAccount } from "../use-account";

// The cPanel-style "home" for one hosting account: a grid of big, clearly
// labeled feature tiles, each its own page — not a WHM-style dense admin
// table with a dozen tiny dialog-trigger icons crammed into one row.
function FeatureCard({
  href,
  icon: Icon,
  title,
  description,
}: {
  href: string;
  icon: typeof Mail;
  title: string;
  description: string;
}) {
  return (
    <Link href={href}>
      <Card className="h-full transition-colors hover:border-primary/40 hover:bg-muted/40">
        <CardContent className="flex flex-col items-start gap-3 p-5">
          <span className="flex h-11 w-11 items-center justify-center rounded-xl bg-primary/12 text-primary">
            <Icon className="h-5 w-5" />
          </span>
          <div>
            <p className="font-medium">{title}</p>
            <p className="text-sm text-muted-foreground">{description}</p>
          </div>
        </CardContent>
      </Card>
    </Link>
  );
}

export default function AccountHubPage() {
  const { id } = useParams<{ id: string }>();
  const qc = useQueryClient();
  const { account, isLoading } = useAccount(id);

  const ssl = useMutation({
    mutationFn: () => issueSSL(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["accounts"] }),
  });

  const backLink = (
    <Link
      href="/accounts"
      className="mb-4 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
    >
      <ArrowLeft className="h-4 w-4" /> Accounts
    </Link>
  );

  if (isLoading) {
    return (
      <div>
        {backLink}
        <Skeleton className="mb-6 h-9 w-64" />
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {[0, 1, 2, 3, 4, 5].map((i) => (
            <Skeleton key={i} className="h-32 rounded-xl" />
          ))}
        </div>
      </div>
    );
  }

  if (!account) {
    return (
      <div>
        {backLink}
        <p className="text-sm text-muted-foreground">Account not found.</p>
      </div>
    );
  }

  const active = account.status === "active";
  const sslIssued = account.ssl_status === "active";
  const sslIssuing = account.ssl_status === "issuing";

  return (
    <div>
      {backLink}
      <PageHeader title={account.username!} description={account.primary_domain!}>
        <Badge variant={active ? "success" : "secondary"}>{account.status}</Badge>
      </PageHeader>

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <FeatureCard
          href={`/accounts/${id}/files`}
          icon={FolderTree}
          title="File Manager"
          description="Browse, edit, and upload files."
        />
        <FeatureCard
          href={`/accounts/${id}/databases`}
          icon={Database}
          title="Databases"
          description="MariaDB databases and phpMyAdmin/Adminer."
        />
        <FeatureCard
          href={`/accounts/${id}/mail`}
          icon={Mail}
          title="Email Accounts"
          description="Mailboxes at this domain."
        />
        <FeatureCard
          href={`/accounts/${id}/ftp`}
          icon={FolderUp}
          title="FTP Accounts"
          description="Pure-FTPd virtual users."
        />
        <FeatureCard
          href={`/accounts/${id}/dns`}
          icon={Globe}
          title="DNS Zone Editor"
          description="A, MX, TXT, and other records."
        />
        <FeatureCard
          href={`/accounts/${id}/cron`}
          icon={Clock}
          title="Cron Jobs"
          description="Scheduled tasks for this account."
        />
        <FeatureCard
          href={`/accounts/${id}/php`}
          icon={Settings2}
          title="PHP Settings"
          description="Version and php.ini overrides."
        />
        {active && (
          <FeatureCard
            href={`/accounts/${id}/terminal`}
            icon={SquareTerminal}
            title="Terminal"
            description="Web-based shell as this account's user."
          />
        )}
      </div>

      <Card className="mt-4">
        <CardContent className="flex flex-col items-start justify-between gap-3 p-5 sm:flex-row sm:items-center">
          <div className="flex items-center gap-3">
            <span className="flex h-11 w-11 shrink-0 items-center justify-center rounded-xl bg-primary/12 text-primary">
              {sslIssued ? <Lock className="h-5 w-5" /> : <LockOpen className="h-5 w-5" />}
            </span>
            <div>
              <p className="font-medium">SSL Certificate</p>
              <p className="text-sm text-muted-foreground">
                {sslIssued
                  ? `Active${account.ssl_expires_at ? ` · expires ${new Date(account.ssl_expires_at).toLocaleDateString()}` : ""}`
                  : sslIssuing
                    ? "Issuing…"
                    : "Not issued yet — free via Let's Encrypt."}
              </p>
            </div>
          </div>
          {!sslIssued && !sslIssuing && (
            <Button size="sm" disabled={!active || ssl.isPending} onClick={() => ssl.mutate()}>
              {ssl.isPending ? "Requesting…" : "Issue certificate"}
            </Button>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
