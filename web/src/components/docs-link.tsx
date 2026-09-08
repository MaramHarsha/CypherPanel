// The link out to the public documentation site (documentation-site.md).
//
// It is CHROME, not navigation, and that placement is a rule rather than a
// preference: ui-principles §4 fixes the top bar at four items — Projects,
// Servers, Templates, Settings — and the inbox bell already records why a fifth
// is refused. So this sits in the right-hand control cluster beside the bell,
// the account menu and the theme toggle, which is where the controls that lead
// somewhere other than a panel screen belong.
//
// It opens in a new tab deliberately. An operator reading a guide is almost
// always mid-task in the panel, and replacing the screen they were working on
// with a documentation page loses their place.
import { BookOpen } from "lucide-react";
import { DOCS_URL } from "@/lib/docs";

export function DocsLink() {
  return (
    <a
      href={DOCS_URL}
      target="_blank"
      rel="noreferrer noopener"
      title="Documentation"
      aria-label="Documentation (opens in a new tab)"
      className="flex h-[34px] w-[34px] items-center justify-center rounded-full border border-border-input bg-surface text-text-mid hover:border-border-strong hover:text-text"
    >
      <BookOpen className="h-4 w-4" aria-hidden />
    </a>
  );
}
