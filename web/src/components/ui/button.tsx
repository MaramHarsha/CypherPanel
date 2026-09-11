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
//   · A disabled pill NAMES ITS REASON (`disabledReason`), and stays in the
//     tab order so a keyboard or screen-reader user can reach that reason —
//     canvas 14g: "with the reason in a tooltip, never contrast alone". It is
//     `aria-disabled` rather than `disabled`, because a natively disabled
//     button is unfocusable and fires no pointer events, which is exactly
//     what would keep the tooltip from ever opening.
import { cva, type VariantProps } from "class-variance-authority";
import { type ButtonHTMLAttributes, type MouseEvent, forwardRef } from "react";
import { Tooltip } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

// 10b's disabled pill, and the two conditions on it.
//
// It belongs to buttons that are a filled or outlined pill at rest — a `ghost`
// is a word, and turning a word into a grey lozenge mid-request is a bigger
// event on screen than the request itself.
//
// It also must not claim the BUSY state. ActionButton holds the button inert
// while an action is in flight, but 10b draws busy as *the same pill* at .75
// with a spinner; only `aria-busy` separates the two at the DOM level, so the
// fill is scoped to inert-and-not-busy rather than to inert alone. Inert is
// spelled two ways — `:disabled` for the plain prop and `[aria-disabled]` for
// the reasoned one — and the selectors are written out in full on purpose:
// Tailwind extracts class names by scanning source text, so a variant
// assembled from a template literal is never generated.
const OFF =
  "[&:disabled:not([aria-busy=true])]:bg-border-subtle " +
  "[&:disabled:not([aria-busy=true])]:text-disabled-fg " +
  "[&:disabled:not([aria-busy=true])]:border-transparent " +
  "[&[aria-disabled=true]:not([aria-busy=true])]:bg-border-subtle " +
  "[&[aria-disabled=true]:not([aria-busy=true])]:text-disabled-fg " +
  "[&[aria-disabled=true]:not([aria-busy=true])]:border-transparent";

const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 rounded-full font-semibold whitespace-nowrap " +
    "transition-[background-color,box-shadow,border-color,color] duration-150 " +
    "disabled:pointer-events-none disabled:shadow-none " +
    // An inert pill neither lifts nor invites: the pointer stays an arrow and
    // hover adds no shadow, but pointer events stay on so the reason can show.
    "aria-disabled:cursor-default aria-disabled:shadow-none aria-disabled:hover:shadow-none",
  {
    variants: {
      variant: {
        primary:
          "bg-primary text-primary-fg hover:bg-primary-hover hover:shadow-lift aria-busy:hover:bg-primary " +
          OFF,
        accent:
          "bg-accent text-accent-fg hover:bg-accent-hover hover:shadow-lift aria-busy:hover:bg-accent " + OFF,
        secondary:
          "border-[1.5px] border-border-strong bg-transparent text-text hover:bg-raised " +
          "aria-busy:hover:bg-transparent " +
          OFF,
        // A ghost has no fill to grey out; it only loses its voice.
        ghost:
          "font-medium text-text-mid hover:bg-raised hover:text-text " +
          "aria-disabled:hover:bg-transparent " +
          "[&:disabled:not([aria-busy=true])]:text-text-disabled " +
          "[&[aria-disabled=true]:not([aria-busy=true])]:text-text-disabled " +
          "[&[aria-disabled=true]:not([aria-busy=true]):hover]:text-text-disabled",
        danger:
          "border-[1.5px] border-status-error bg-status-error/[0.08] text-danger hover:bg-status-error hover:text-white " +
          "aria-busy:hover:bg-status-error/[0.08] aria-busy:hover:text-danger " +
          OFF,
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
    VariantProps<typeof buttonVariants> {
  /**
   * Why the button cannot be pressed — "viewers can't deploy", "freeze window
   * until Mon 08:00", "enter a repository first". Presence makes the button
   * inert and names the reason in a tooltip that opens on hover AND on focus:
   * a disabled control that does not say why is a dead end (canvas 10b/14g).
   */
  disabledReason?: string;
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  (
    { className, variant, size, type, disabledReason, disabled, onClick, "aria-disabled": ariaDisabled, ...props },
    ref,
  ) => {
    // Inert = reasoned-disabled, or busy (ActionButton passes aria-disabled
    // while an action is in flight so focus is not thrown to <body> mid-click,
    // which is what a native `disabled` does to the element that has it).
    const inert = Boolean(disabledReason) || ariaDisabled === true || ariaDisabled === "true";
    const button = (
      <button
        ref={ref}
        type={type ?? "button"}
        // The reason wins over the plain prop: a reasoned pill has to stay
        // focusable, which `disabled` would take away.
        disabled={disabled && !disabledReason}
        aria-disabled={inert || undefined}
        onClick={
          inert
            ? (e: MouseEvent<HTMLButtonElement>) => {
                // Swallowing the click also cancels a form's implicit submit,
                // so an inert submit pill does not post on Enter in a field.
                e.preventDefault();
                e.stopPropagation();
              }
            : onClick
        }
        className={cn(buttonVariants({ variant, size }), className)}
        {...props}
      />
    );
    if (!disabledReason) return button;
    return <Tooltip content={disabledReason}>{button}</Tooltip>;
  },
);
Button.displayName = "Button";
