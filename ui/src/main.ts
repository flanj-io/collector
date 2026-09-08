import { createApp } from 'vue';

// The canonical Flanj token layer, vendored from docs/design/tokens.css — imported
// FIRST so every surface rule below it resolves against it. tokens-pending.css adds
// the single family the canonical set does not define yet; its header says why and
// how it goes away.
import './tokens.css';
import './tokens-pending.css';

import App from './App.vue';
import { applyTheme, loadThemePref } from './theme';

// Stamp the resolved theme on <html> before the app mounts. Light by default
// (ux-design-v2 §3): no stored choice = light, and the dark palette lives under
// [data-theme="dark"] only — there is no OS-following state any more.
applyTheme(loadThemePref());

createApp(App).mount('#app');
