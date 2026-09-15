"use client";

// hardNavigate leaves the SPA with a full document load. Its own module
// because jsdom makes `location.assign` non-configurable: a test cannot spy on
// it, but it can mock this.
export function hardNavigate(url: string): void {
  window.location.assign(url);
}
