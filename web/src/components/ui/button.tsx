// Buttons are pills in Mission Control. The primary action is an ink pill
// (paper pill in dark — it inverts, per the 4e token table); `accent` is the
// signal-orange pill, reserved for the one moment a screen has a single
// unmissable action (sign in, the golden path's next step).
//
// The six states of canvas 10b/13al live in two places on purpose: idle, hover
// and disabled are here, because every button has them; busy, success and
// failed are ActionButton's, because they belong to an action with a lifecycle.
//   · HOVER lifts — a 2px/8px shadow — and never changes size.
//   · DISABLED is a filled paper-grey pill, not a faded live one: "unavailable"
//     should look like a different object, not a dimmed version of the thing
//     you wanted. The label holds one value in both themes (--disabled-fg).
import { cva, type VariantProps } from "class-variance-authority";
import { type ButtonHTMLAttributes, forwardRef } from "react";
import { cn } from "@/lib/utils";

// 10b's disabled pill, and the two conditions on it.
//
// It belongs to buttons that are a filled or outlined pill at rest — a `ghost`
// is a word, and turning a word into a grey lozenge mid-request is a bigger
// event on screen than the request itself.
//
// It also must not claim the BUSY state. ActionButton disables the button while
// an action is in flight, but 10b draws busy as *the same pill* at .75 with a
// spinner; only `aria-busy` separates the two at the DOM level, so the fill is
// scoped to disabled-and-not-busy rather than to `disabled` alone. These
// selectors are spelled out in full on purpose: Tailwind extracts class names
// by scanning source text, so a variant assembled from a template literal is
// never generated.
const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 rounded-full font-semibold whitespace-nowrap " +
    "transition-[background-color,box-shadow,border-color,color] duration-150 " +
    "disabled:pointer-events-none disabled:shadow-none",
  {
    variants: {
      variant: {
        primary:
          "bg-primary text-primary-fg hover:bg-primary-hover hover:shadow-lift " +
          "[&:disabled:not([aria-busy=true])]:bg-border-subtle " +
          "[&:disabled:not([aria-busy=true])]:text-disabled-fg " +
          "[&:disabled:not([aria-busy=true])]:border-transparent",
        accent:
          "bg-accent text-accent-fg hover:bg-accent-hover hover:shadow-lift " +
          "[&:disabled:not([aria-busy=true])]:bg-border-subtle " +
          "[&:disabled:not([aria-busy=true])]:text-disabled-fg " +
          "[&:disabled:not([aria-busy=true])]:border-transparent",
        secondary:
          "border-[1.5px] border-border-strong bg-transparent text-text hover:bg-raised " +
          "[&:disabled:not([aria-busy=true])]:bg-border-subtle " +
          "[&:disabled:not([aria-busy=true])]:text-disabled-fg " +
          "[&:disabled:not([aria-busy=true])]:border-transparent",
        // A ghost has no fill to grey out; it only loses its voice.
        ghost:
          "font-medium text-text-mid hover:bg-raised hover:text-text " +
          "[&:disabled:not([aria-busy=true])]:text-text-disabled",
        danger:
          "border-[1.5px] border-status-error bg-status-error/[0.08] text-danger hover:bg-status-error hover:text-white " +
          "[&:disabled:not([aria-busy=true])]:bg-border-subtle " +
          "[&:disabled:not([aria-busy=true])]:text-disabled-fg " +
          "[&:disabled:not([aria-busy=true])]:border-transparent",
      },
      size: {
        sm: "h-7 px-3 text-xs",
        // 9px 18px at 12.5px — canvas 10b's geometry for every state.
        md: "h-[34px] px-[18px] text-[12.5px]",
        lg: "h-10 px-5 text-[13px]",
      },
    },
    defaultVariants: { variant: "secondary", size: "md" },
  },
);

export interface ButtonProps
  extends ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, type, ...props }, ref) => (
    <button
      ref={ref}
      type={type ?? "button"}
      className={cn(buttonVariants({ variant, size }), className)}
      {...props}
    />
  ),
);
Button.displayName = "Button";
