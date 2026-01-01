import * as React from "react";
import { cn } from "@/lib/utils";

export interface SeparatorProps extends React.HTMLAttributes<HTMLDivElement> {
  orientation?: "horizontal" | "vertical";
}

export function Separator({ className, orientation = "horizontal", role = "separator", ...props }: SeparatorProps) {
  const isVertical = orientation === "vertical";
  return (
    <div
      role={role}
      aria-orientation={orientation}
      className={cn(
        "bg-border",
        isVertical ? "w-px h-full" : "h-px w-full",
        className
      )}
      {...props}
    />
  );
}
