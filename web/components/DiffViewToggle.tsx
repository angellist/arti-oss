"use client";

import { useEffect } from "react";

import { installDiffViewToggle } from "@/lib/diffView";

export default function DiffViewToggle() {
  useEffect(() => installDiffViewToggle(), []);
  return null;
}
