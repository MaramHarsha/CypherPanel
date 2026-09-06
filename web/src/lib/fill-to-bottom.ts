// "On a phone the pane IS the screen" (canvas 14d) expressed as arithmetic,
// once. Both log panes — an application's and a compose stack's — run from
// wherever they start to the top of the bottom bar, and a second copy of this
// measurement is a second chance to get the reserve wrong.
import { useLayoutEffect, useState } from "react";

/** Tailwind's `sm` — below it the pane takes the phone layout. */
const PHONE = "(max-width: 639px)";

/**
 * The masthead above the pane is not the pane's to remove, and its height is
 * not a constant — the resource name wraps, the redeploy chip comes and goes,
 * the tail note appears when the container stops — so the height is measured
 * rather than guessed: the viewport, less the pane's own offset, less the
 * reserve `<main>` keeps for the bar (which already includes the notch inset).
 * Re-measured when the window or anything above the pane changes size. From
 * `sm` up the style is cleared and the pane's own classes size it.
 *
 * Returns a callback ref: the pane mounts only once its resource has loaded,
 * so a ref object read in an effect on mount would still be empty.
 */
export function useFillToBottom(): (el: HTMLDivElement | null) => void {
  const [el, setEl] = useState<HTMLDivElement | null>(null);
  const [phone, setPhone] = useState(() => window.matchMedia(PHONE).matches);
  useLayoutEffect(() => {
    const mq = window.matchMedia(PHONE);
    const sync = () => setPhone(mq.matches);
    mq.addEventListener("change", sync);
    return () => mq.removeEventListener("change", sync);
  }, []);
  useLayoutEffect(() => {
    if (!el) return;
    if (!phone) {
      el.style.height = "";
      return;
    }
    const apply = () => {
      const top = el.getBoundingClientRect().top + window.scrollY;
      const main = document.getElementById("main");
      const reserve = main ? parseFloat(getComputedStyle(main).paddingBottom) || 0 : 0;
      el.style.height = `${Math.max(280, window.innerHeight - top - reserve)}px`;
    };
    apply();
    window.addEventListener("resize", apply);
    // Whatever sits above the pane shares its parent; a change in the
    // parent's size is a change in where the pane starts. Setting the same
    // height again does not resize anything, so this cannot feed itself.
    const above = el.parentElement;
    const ro = above ? new ResizeObserver(apply) : undefined;
    if (above) ro?.observe(above);
    return () => {
      window.removeEventListener("resize", apply);
      ro?.disconnect();
      el.style.height = "";
    };
  }, [el, phone]);
  return setEl;
}
