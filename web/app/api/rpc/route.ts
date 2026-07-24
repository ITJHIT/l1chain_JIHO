import { NextRequest, NextResponse } from "next/server";

// Same-origin JSON-RPC proxy.
//
// WHY THIS EXISTS: go-l1-chain/rpc/server.go only sets a Content-Type header on
// its responses (see writeJSON) and it rejects any method other than POST
// (ServeHTTP returns codeInvalidRequest for non-POST). It sends NO
// Access-Control-Allow-Origin header and handles no CORS pre-flight (OPTIONS).
// A browser making a cross-origin fetch to the Go node would therefore be
// blocked by the same-origin policy. This route runs on the Next.js server
// (same origin as the app), forwards the JSON-RPC envelope verbatim to the Go
// node, and returns its response verbatim — no CORS needed on the Go side.

const RPC_URL =
  process.env.RPC_URL || process.env.NEXT_PUBLIC_RPC_URL || "http://127.0.0.1:8545";

export const dynamic = "force-dynamic";

export async function POST(req: NextRequest) {
  let body: unknown;
  try {
    body = await req.json();
  } catch {
    return NextResponse.json(
      { jsonrpc: "2.0", id: null, error: { code: -32700, message: "parse error" } },
      { status: 400 },
    );
  }

  try {
    const upstream = await fetch(RPC_URL, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
      cache: "no-store",
    });
    const text = await upstream.text();
    return new NextResponse(text, {
      status: upstream.status,
      headers: { "Content-Type": "application/json" },
    });
  } catch (err) {
    return NextResponse.json(
      {
        jsonrpc: "2.0",
        id: null,
        error: { code: -32603, message: `upstream unreachable: ${String(err)}` },
      },
      { status: 502 },
    );
  }
}
