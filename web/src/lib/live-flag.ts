// Tiny module-level flag bridging the SSE stream's health into the query
// client's default refetchInterval (poll only while the stream is down).
let open = false;

export function isSseOpen(): boolean {
  return open;
}

export function setSseOpen(v: boolean): void {
  open = v;
}
