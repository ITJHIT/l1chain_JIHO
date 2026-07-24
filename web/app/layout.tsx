import type { Metadata } from "next";
import Link from "next/link";
import "./globals.css";

export const metadata: Metadata = {
  title: "L1 Block Explorer",
  description: "Block explorer for the go-l1-chain JSON-RPC node",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>
        <nav className="top">
          <span className="brand">
            <Link href="/">L1 Explorer</Link>
          </span>
          <Link href="/">Home</Link>
          <Link href="/address">Address</Link>
          <Link href="/tx">Tx</Link>
          <Link href="/send">Send</Link>
        </nav>
        <div className="container">{children}</div>
      </body>
    </html>
  );
}
