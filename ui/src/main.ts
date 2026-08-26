import { createApp } from 'vue';
import App from './App.vue';
import { applyTheme, loadThemePref } from './theme';

// Stamp the resolved theme on <html> before the app mounts. Light by default
// (ux-design-v2 §3): no stored choice = light, and the dark palette lives under
// [data-theme="dark"] only — there is no OS-following state any more.
applyTheme(loadThemePref());

createApp(App).mount('#app');
