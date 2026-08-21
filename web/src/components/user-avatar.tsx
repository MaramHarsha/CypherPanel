// The person, wherever they appear: the top-bar chip and the profile header.
//
// The bytes are fetched rather than pointed at, because `GET /users/{id}/avatar`
// needs the bearer token — an `<img src>` the browser resolves on its own would
// go out unauthenticated, and a token in the query string would write the
// credential into history and logs.
//
// They become a `data:` URL rather than an object URL: the panel's CSP is
// `img-src 'self' data:`, so a `blob:` URL is refused, and widening a policy
// web-ui-design.md §5 calls a deliberate security property is a bad trade for a
// bounded image. It also removes the object-URL lifetime question entirely.
//
// It rides TanStack Query so the two call sites share one cache entry and one
// request — and so an upload can invalidate both by touching a single key.
import { useQuery } from "@tanstack/react-query";
import { getGetAvatarUrl } from "@/api/gen/auth/auth";
import { apiBlob } from "@/api/client";
import { cn } from "@/lib/utils";

/** The one key both the chip and the profile header read. */
export function avatarQueryKey(userId: string | undefined) {
  return ["avatar", userId ?? ""] as const;
}

export function useAvatar(userId: string | undefined) {
  return useQuery({
    queryKey: avatarQueryKey(userId),
    enabled: Boolean(userId),
    // The image only changes through this panel's own upload and removal, both
    // of which invalidate this key — so there is nothing for a refetch to find.
    staleTime: Infinity,
    queryFn: async () => {
      const blob = await apiBlob(getGetAvatarUrl(userId as string));
      if (!blob) return null;
      return new Promise<string>((resolve, reject) => {
        const reader = new FileReader();
        reader.onload = () => resolve(String(reader.result));
        reader.onerror = () => reject(reader.error);
        reader.readAsDataURL(blob);
      });
    },
  });
}

/** Initials from the name once there is one, else from the address. */
export function initialsFor(name: string | undefined, email: string | undefined): string {
  const source = (name ?? "").trim() || (email ?? "").split("@")[0] || "";
  const parts = source.split(/[\s.\-_+]/).filter(Boolean);
  const first = parts[0]?.[0] ?? source[0] ?? "·";
  const second = parts.length > 1 ? (parts[1]?.[0] ?? "") : (source[1] ?? "");
  return (first + second).toUpperCase() || "·";
}

export function UserAvatar({
  userId,
  name,
  email,
  className,
  textClassName,
}: {
  userId?: string;
  name?: string;
  email?: string;
  /** Sizing lives with the caller — the chip is 22px, the profile header 56px. */
  className?: string;
  textClassName?: string;
}) {
  const { data: src } = useAvatar(userId);
  if (src) {
    return <img src={src} alt="" className={cn("flex-none rounded-full object-cover", className)} />;
  }
  return (
    <span
      aria-hidden
      className={cn(
        "flex flex-none items-center justify-center rounded-full bg-primary font-mono uppercase text-primary-fg",
        className,
        textClassName,
      )}
    >
      {initialsFor(name, email)}
    </span>
  );
}
