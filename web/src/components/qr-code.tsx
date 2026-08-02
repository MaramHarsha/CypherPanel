// A QR code rendered as inline SVG. Drawn client-side from the otpauth:// URI
// the panel returns, so an enrollment secret never travels to a third-party
// image service — the whole point of self-hosting.
import qrcode from "qrcode-generator";

export function QRCode({ value, size = 176, label }: { value: string; size?: number; label?: string }) {
  // Type 0 = smallest version that fits; "M" tolerates ~15% damage, the usual
  // choice for screen-displayed codes.
  const qr = qrcode(0, "M");
  qr.addData(value);
  qr.make();
  const count = qr.getModuleCount();

  // One path for every dark module beats one <rect> each: far fewer nodes.
  let path = "";
  for (let row = 0; row < count; row++) {
    for (let col = 0; col < count; col++) {
      if (qr.isDark(row, col)) path += `M${col},${row}h1v1h-1z`;
    }
  }

  return (
    <svg
      width={size}
      height={size}
      viewBox={`-1 -1 ${count + 2} ${count + 2}`}
      role="img"
      aria-label={label ?? "QR code"}
      shapeRendering="crispEdges"
      className="rounded-md bg-white p-1"
    >
      <path d={path} fill="#000" />
    </svg>
  );
}
