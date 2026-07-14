"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { hasSession } from "@/lib/api";

export default function Home() {
  const router = useRouter();
  useEffect(() => {
    router.replace(hasSession() ? "/dashboard" : "/login");
  }, [router]);
  return null;
}
