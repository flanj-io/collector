import { createApp } from 'vue';

// The canonical Flanj token layer, vendored (see src/tokens.css) — imported
// FIRST so every surface rule below it resolves against it. It is the only token
// file: the Blueprint set carries the --ok family, so the pending quarantine that
// used to sit beside it is gone.
import './tokens.css';

import App from './App.vue';
import { applyTheme, loadThemePref } from './theme';

// Stamp the resolved theme on <html> before the app mounts. Light by default:
// no stored choice = light, and the dark palette lives under
// [data-flanj-theme="dark"] only — there is no OS-following state any more.
applyTheme(loadThemePref());

createApp(App).mount('#app');
