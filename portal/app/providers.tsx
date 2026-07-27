"use client";

// Client boundary for Mantine. Kept separate so the root layout stays a server
// component (it must read the per-request CSP nonce). Forced to the dark scheme —
// the operator console is dark-only.

import { MantineProvider } from "@mantine/core";
import { Notifications } from "@mantine/notifications";
import { theme } from "@/lib/theme";

export default function Providers({ children }: { children: React.ReactNode }) {
  return (
    <MantineProvider theme={theme} forceColorScheme="dark">
      <Notifications position="top-right" />
      {children}
    </MantineProvider>
  );
}
