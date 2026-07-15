"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { ArrowLeft } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { accessTokenForWS } from "@/lib/api";
import "@xterm/xterm/css/xterm.css";

export default function TerminalPage() {
  const { id } = useParams<{ id: string }>();
  const containerRef = useRef<HTMLDivElement>(null);
  const [status, setStatus] = useState("connecting…");

  useEffect(() => {
    let disposed = false;
    let ws: WebSocket | null = null;
    // Cleanup holder so the async setup can register teardown.
    let cleanup = () => {};

    (async () => {
      // xterm is browser-only — import dynamically so it never runs on the server.
      const { Terminal } = await import("@xterm/xterm");
      const { FitAddon } = await import("@xterm/addon-fit");
      if (disposed || !containerRef.current) return;

      const term = new Terminal({
        cursorBlink: true,
        fontFamily: "var(--font-mono), monospace",
        fontSize: 13,
        theme: { background: "#0b0b12" },
      });
      const fit = new FitAddon();
      term.loadAddon(fit);
      term.open(containerRef.current);
      fit.fit();

      const token = await accessTokenForWS();
      if (!token) {
        term.write("\r\n\x1b[31mNot authenticated — reload the page.\x1b[0m\r\n");
        setStatus("unauthenticated");
        return;
      }
      const proto = location.protocol === "https:" ? "wss" : "ws";
      ws = new WebSocket(
        `${proto}://${location.host}/api/v1/admin/accounts/${id}/terminal?token=${encodeURIComponent(token)}`,
      );

      const send = (m: object) => ws?.readyState === WebSocket.OPEN && ws.send(JSON.stringify(m));
      const encode = (s: string) => Array.from(new TextEncoder().encode(s));

      ws.onopen = () => {
        setStatus("connected");
        const d = term.rows && term.cols ? { cols: term.cols, rows: term.rows } : {};
        send({ type: "resize", ...d });
      };
      ws.onmessage = (ev) => {
        try {
          const msg = JSON.parse(ev.data as string) as { type: string; data?: string };
          if (msg.type === "data" && msg.data) {
            // Go marshals []byte as base64.
            term.write(Uint8Array.from(atob(msg.data), (ch) => ch.charCodeAt(0)));
          } else if (msg.type === "close") {
            term.write("\r\n\x1b[33m[session closed]\x1b[0m\r\n");
            setStatus("closed");
          }
        } catch {
          /* ignore malformed frame */
        }
      };
      ws.onclose = () => setStatus("closed");
      ws.onerror = () => setStatus("error");

      term.onData((data) => send({ type: "data", data: btoa(String.fromCharCode(...encode(data))) }));
      const onResize = () => {
        fit.fit();
        send({ type: "resize", cols: term.cols, rows: term.rows });
      };
      window.addEventListener("resize", onResize);

      cleanup = () => {
        window.removeEventListener("resize", onResize);
        ws?.close();
        term.dispose();
      };
    })();

    return () => {
      disposed = true;
      cleanup();
    };
  }, [id]);

  return (
    <div>
      <Link
        href="/accounts"
        className="mb-4 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="h-4 w-4" /> Accounts
      </Link>
      <PageHeader title="Terminal" description={`Shell as the account user · ${status}`} />
      <div
        ref={containerRef}
        className="h-[70vh] w-full overflow-hidden rounded-xl border border-border bg-[#0b0b12] p-2"
      />
    </div>
  );
}
