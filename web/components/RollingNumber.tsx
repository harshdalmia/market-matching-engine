"use client";

import { AnimatePresence, motion } from "motion/react";
import { useEffect, useRef } from "react";

/** Width per character class, so slots stay aligned without a monospace hack. */
function slotWidth(char: string): string {
  if (char === "." || char === ",") return "0.3em";
  if (char === " ") return "0.28em";
  if (/\d/.test(char)) return "0.62em";
  return "0.56em";
}

function Slot({ char, direction }: { char: string; direction: number }) {
  return (
    <span
      className="relative inline-block overflow-hidden"
      style={{ width: slotWidth(char), height: "1.16em" }}
    >
      <AnimatePresence initial={false}>
        <motion.span
          key={char}
          initial={{ y: direction >= 0 ? "-105%" : "105%", opacity: 0 }}
          animate={{ y: "0%", opacity: 1 }}
          exit={{ y: direction >= 0 ? "105%" : "-105%", opacity: 0 }}
          transition={{ duration: 0.26, ease: [0.16, 1, 0.3, 1] }}
          className="absolute inset-0 flex items-center justify-center"
        >
          {char}
        </motion.span>
      </AnimatePresence>
    </span>
  );
}

interface RollingNumberProps {
  value: number;
  format?: (value: number) => string;
  className?: string;
}

/**
 * Renders a number as per-character slots that roll vertically when they change,
 * upward when the value rises and downward when it falls.
 *
 * The animated glyphs are hidden from assistive tech and the plain formatted
 * string is exposed instead, so the value is announced once rather than as a
 * stream of individual digits.
 */
export function RollingNumber({
  value,
  format = (v) => v.toFixed(2),
  className = "",
}: RollingNumberProps) {
  const previous = useRef(value);
  const direction = value >= previous.current ? 1 : -1;

  useEffect(() => {
    previous.current = value;
  }, [value]);

  const text = format(value);

  return (
    <span className={`numeric inline-flex items-center ${className}`}>
      <span className="sr-only">{text}</span>
      <span aria-hidden="true" className="inline-flex items-center">
        {text.split("").map((char, i) => (
          <Slot key={i} char={char} direction={direction} />
        ))}
      </span>
    </span>
  );
}
