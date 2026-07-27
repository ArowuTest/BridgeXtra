import { createTheme } from "@mantine/core";

// The operator console is dark, information-dense (MantineProvider is forced to the
// dark scheme in Providers). Keep the theme thin — inherit the existing globals.css
// font stack and lean on Mantine's dark palette; the surface aesthetic is refined
// against the Feed & Recon Health mockup as the workspaces land.
export const theme = createTheme({
  primaryColor: "teal",
  defaultRadius: "sm",
  cursorType: "pointer",
  fontFamily:
    "system-ui, -apple-system, Segoe UI, Roboto, Helvetica, Arial, sans-serif",
  fontFamilyMonospace: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
});
