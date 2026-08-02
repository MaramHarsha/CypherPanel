// Buttons are pills in Mission Control. The primary action is an ink pill
// (paper pill in dark — it inverts, per the 4e token table); `accent` is the
// signal-orange pill, reserved for the one moment a screen has a single
// unmissable action (sign in, the golden path's next step).
import { cva, type VariantProps } from "class-variance-authority";
import { type ButtonHTMLAttributes, forwardRef } from "react";
import { cn } from "@/lib/utils";

const buttonVariants = cva(
  "inline-flex items-center justify-center gap-1.5 rounded-full font-semibold transition-colors duration-150 disabled:pointer-events-none disabled:opacity-50 whitespace-nowrap",
  {
    variants: {
      variant: {
        primary: "bg-primary text-primary-fg hover:bg-primary-hover",
        accent: "bg-accent text-accent-fg hover:bg-accent-hover",
        secondary: "border border-border-strong bg-transparent text-text hover:bg-raised",
        ghost: "font-medium text-text-mid hover:bg-raised hover:text-text",
        danger: "border border-danger/40 bg-transparent text-danger hover:bg-danger hover:text-white",
      },
      size: {
        sm: "h-7 px-3 text-xs",
        md: "h-8 px-4 text-[12.5px]",
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
