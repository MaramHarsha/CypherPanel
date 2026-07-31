"use client";

import { useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowLeft,
  File as FileIcon,
  FilePlus,
  Folder,
  FolderPlus,
  Save,
  Trash2,
  X,
} from "lucide-react";
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
import { Skeleton } from "@/components/ui/skeleton";
import { PageHeader } from "@/components/page-header";
import {
  ApiError,
  fmDelete,
  fmList,
  fmMkdir,
  fmRead,
  fmWrite,
  type FSEntry,
} from "@/lib/api";

function join(dir: string, name: string): string {
  return dir ? `${dir}/${name}` : name;
}

// Dialog-based name prompt, replacing the native window.prompt() so file/folder
// creation matches the rest of the app's custom Dialog styling instead of
// dropping into an unstyled browser popup.
function NewEntryDialog({
  label,
  icon: Icon,
  onCreate,
  pending,
}: {
  label: string;
  icon: typeof FolderPlus;
  onCreate: (name: string) => void;
  pending: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) setName("");
      }}
    >
      <DialogTrigger render={<Button variant="outline" size="sm" />}>
        <Icon className="h-4 w-4" /> {label}
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>New {label.toLowerCase()}</DialogTitle>
          <DialogDescription>Created in the current directory.</DialogDescription>
        </DialogHeader>
        <div className="grid gap-1.5">
          <Label htmlFor="new-entry-name">Name</Label>
          <Input
            id="new-entry-name"
            autoFocus
            value={name}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && name.trim()) {
                onCreate(name.trim());
                setOpen(false);
              }
            }}
          />
        </div>
        <DialogFooter>
          <Button
            disabled={!name.trim() || pending}
            onClick={() => {
              onCreate(name.trim());
              setOpen(false);
            }}
          >
            Create
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function DeleteEntryButton({
  name,
  onDelete,
}: {
  name: string;
  onDelete: () => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <AlertDialog open={open} onOpenChange={setOpen}>
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label={`Delete ${name}`}
        onClick={() => setOpen(true)}
      >
        <Trash2 className="h-3.5 w-3.5 text-destructive" />
      </Button>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete {name}?</AlertDialogTitle>
          <AlertDialogDescription>This cannot be undone.</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            onClick={(e) => {
              e.preventDefault();
              onDelete();
              setOpen(false);
            }}
          >
            Delete
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

export default function FileManagerPage() {
  const { id } = useParams<{ id: string }>();
  const qc = useQueryClient();
  const [cwd, setCwd] = useState("");
  const [editing, setEditing] = useState<{ path: string; content: string } | null>(null);
  const [error, setError] = useState<string | null>(null);

  const key = ["files", id, cwd];
  const { data, isLoading } = useQuery({
    queryKey: key,
    queryFn: () => fmList(id, cwd),
  });

  const open = useMutation({
    mutationFn: (path: string) => fmRead(id, path).then((r) => ({ path, content: r.content })),
    onSuccess: (r) => { setEditing(r); setError(null); },
    onError: (e) => setError(e instanceof ApiError ? e.message : "Could not open file"),
  });
  const save = useMutation({
    mutationFn: () => fmWrite(id, editing!.path, editing!.content),
    onSuccess: () => setError(null),
    onError: (e) => setError(e instanceof ApiError ? e.message : "Could not save"),
  });
  const remove = useMutation({
    mutationFn: (path: string) => fmDelete(id, path),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
    onError: (e) => setError(e instanceof ApiError ? e.message : "Could not delete"),
  });
  const mkdir = useMutation({
    mutationFn: (name: string) => fmMkdir(id, join(cwd, name)),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
    onError: (e) => setError(e instanceof ApiError ? e.message : "Could not create folder"),
  });
  const newFile = useMutation({
    mutationFn: (name: string) => fmWrite(id, join(cwd, name), ""),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
    onError: (e) => setError(e instanceof ApiError ? e.message : "Could not create file"),
  });

  const crumbs = cwd ? cwd.split("/") : [];

  return (
    <div>
      <Link
        href={`/accounts/${id}`}
        className="mb-4 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="h-4 w-4" /> Back
      </Link>
      <PageHeader title="File manager" description="Browse and edit this account's files.">
        <NewEntryDialog
          label="Folder"
          icon={FolderPlus}
          pending={mkdir.isPending}
          onCreate={(name) => mkdir.mutate(name)}
        />
        <NewEntryDialog
          label="File"
          icon={FilePlus}
          pending={newFile.isPending}
          onCreate={(name) => newFile.mutate(name)}
        />
      </PageHeader>

      {/* Breadcrumb */}
      <div className="mb-3 flex flex-wrap items-center gap-1 text-sm">
        <button className="text-primary hover:underline" onClick={() => setCwd("")}>
          ~
        </button>
        {crumbs.map((seg, i) => (
          <span key={i} className="flex items-center gap-1">
            <span className="text-muted-foreground">/</span>
            <button
              className="text-primary hover:underline"
              onClick={() => setCwd(crumbs.slice(0, i + 1).join("/"))}
            >
              {seg}
            </button>
          </span>
        ))}
      </div>

      {error && <p className="mb-3 text-sm text-destructive">{error}</p>}

      <div className="grid gap-4 lg:grid-cols-[1fr_1.4fr]">
        <Card>
          <CardContent className="p-0">
            {isLoading ? (
              <div className="space-y-2 p-4">
                {[0, 1, 2].map((i) => (
                  <Skeleton key={i} className="h-8 w-full" />
                ))}
              </div>
            ) : (data?.entries ?? []).length === 0 ? (
              <p className="py-10 text-center text-sm text-muted-foreground">Empty directory.</p>
            ) : (
              <ul className="divide-y divide-border">
                {(data?.entries ?? []).map((e: FSEntry) => (
                  <li key={e.name} className="flex items-center gap-2 px-3 py-2 hover:bg-muted/50">
                    <button
                      className="flex min-w-0 flex-1 items-center gap-2 text-left"
                      onClick={() =>
                        e.is_dir ? setCwd(join(cwd, e.name)) : open.mutate(join(cwd, e.name))
                      }
                    >
                      {e.is_dir ? (
                        <Folder className="h-4 w-4 shrink-0 text-primary" />
                      ) : (
                        <FileIcon className="h-4 w-4 shrink-0 text-muted-foreground" />
                      )}
                      <span className="truncate text-sm">{e.name}</span>
                    </button>
                    <span className="shrink-0 font-mono text-[11px] text-muted-foreground">
                      {e.is_dir ? "—" : `${e.size}B`}
                    </span>
                    <DeleteEntryButton
                      name={e.name}
                      onDelete={() => remove.mutate(join(cwd, e.name))}
                    />
                  </li>
                ))}
              </ul>
            )}
          </CardContent>
        </Card>

        {editing ? (
          <Card>
            <CardHeader className="flex flex-row items-center justify-between space-y-0">
              <CardTitle className="truncate font-mono text-sm">{editing.path}</CardTitle>
              <div className="flex gap-1">
                <Button size="sm" onClick={() => save.mutate()} disabled={save.isPending}>
                  <Save className="h-4 w-4" /> {save.isPending ? "Saving…" : "Save"}
                </Button>
                <Button variant="ghost" size="icon-sm" aria-label="Close" onClick={() => setEditing(null)}>
                  <X className="h-4 w-4" />
                </Button>
              </div>
            </CardHeader>
            <CardContent>
              <textarea
                className="h-[60vh] w-full resize-none rounded-lg border border-border bg-background p-3 font-mono text-xs outline-none focus:ring-2 focus:ring-ring"
                value={editing.content}
                onChange={(e) => setEditing({ ...editing, content: e.target.value })}
                spellCheck={false}
              />
            </CardContent>
          </Card>
        ) : (
          <Card className="hidden lg:block">
            <CardHeader className="items-center py-16 text-center">
              <FileIcon className="mb-2 h-8 w-8 text-muted-foreground" />
              <CardTitle className="text-base">No file open</CardTitle>
              <CardDescription>Select a text file to view and edit it.</CardDescription>
            </CardHeader>
          </Card>
        )}
      </div>
    </div>
  );
}
