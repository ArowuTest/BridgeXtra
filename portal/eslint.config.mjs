import { FlatCompat } from "@eslint/eslintrc";

const compat = new FlatCompat({ baseDirectory: import.meta.dirname });

// Next.js lint ruleset (core-web-vitals + TypeScript) expressed as flat config
// so `next lint` runs non-interactively in CI. Build artefacts are ignored.
const eslintConfig = [
  ...compat.extends("next/core-web-vitals", "next/typescript"),
  { ignores: [".next/**", "node_modules/**"] },
];

export default eslintConfig;
