import type { Metadata, Viewport } from "next";
import "./globals.css";
import { ToastProvider } from "@/components/ui";

export const metadata: Metadata = {
  title: { default: "CBMP Bank", template: "%s · CBMP Bank" },
  description: "Core Banking Microservices Platform: a 16-service banking system on Kubernetes (DSO202 final project).",
};

export const viewport: Viewport = { themeColor: "#0f4c81", width: "device-width", initialScale: 1 };

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body className="min-h-screen font-sans">
        <ToastProvider>{children}</ToastProvider>
      </body>
    </html>
  );
}
