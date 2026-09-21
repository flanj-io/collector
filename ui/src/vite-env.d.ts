/// <reference types="vite/client" />

declare module '*.vue' {
  import type { DefineComponent } from 'vue';
  const component: DefineComponent<{}, {}, any>;
  export default component;
}

// A top-level `export {}` is required below: without one, this file has no
// import/export of its own and TypeScript treats it as a global SCRIPT, where
// `declare module 'vue' { … }` defines a brand new (and, because it lacks
// everything the real package exports, unusably empty) ambient module named
// "vue" instead of augmenting the real one — every `import { ref } from
// 'vue'` in the app then fails to resolve. Once the file is a module, the
// same block correctly MERGES into vue's own types.
export {};

// @vue/runtime-dom's HTMLAttributes carries no `data-*` index signature, so
// vue-tsc's `checkUnknownProps` (on, deliberately, in tsconfig.json) flags a
// plain `data-label="…"` on a native element as an unknown prop. The Edges
// tables' ≤720px collapse reads that attribute for its stacked-line labels
// (`td::before { content: attr(data-label) }`), so it is a real, load-bearing
// attribute, not a typo the check should be catching. A template-literal
// pattern index signature still lets a genuine unknown prop (a real typo)
// fail the check.
declare module 'vue' {
  interface HTMLAttributes {
    [key: `data-${string}`]: unknown;
  }
}
