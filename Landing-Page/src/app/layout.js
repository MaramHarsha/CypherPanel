import "./globals.css";

export const metadata = {
  title: "CypherPanel - Open-Source cPanel & WHM Alternative",
  description: "A modern, secure, and self-hosted control panel designed to be a direct, feature-complete replacement for cPanel and WHM. Fast, lightweight, and distro-agnostic.",
  keywords: "cPanel, WHM, control panel, open-source, web hosting, Linux server management, self-hosted, Next.js, Go",
  authors: [{ name: "CypherPanel Team" }],
  openGraph: {
    title: "CypherPanel - Open-Source cPanel & WHM Alternative",
    description: "Modern, secure, and API-first control panel designed to be a direct, feature-complete replacement for cPanel and WHM.",
    type: "website",
    locale: "en_US",
  },
};

export default function RootLayout({ children }) {
  return (
    <html lang="en">
      <body>
        {children}
      </body>
    </html>
  );
}
