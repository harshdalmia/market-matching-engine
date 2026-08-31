import type { Metadata, Viewport } from "next";
import { JetBrains_Mono } from "next/font/google";
import "./globals.css";

// The whole terminal is monospace: aligned columns of digits are the point.
const jetbrains = JetBrains_Mono({
  subsets: ["latin"],
  variable: "--font-jetbrains",
  display: "swap",
});

export const metadata: Metadata = {
  title: "Terminal · Matching Engine",
  description:
    "Live order book, candles, depth and order entry for a low-latency Go matching engine.",
};

export const viewport: Viewport = {
  themeColor: "#000000",
};

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en" className={jetbrains.variable}>
      <body className="h-dvh overflow-hidden bg-bg text-txt antialiased">
        {children}
      </body>
    </html>
  );
}
