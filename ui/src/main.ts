import { createApp } from 'vue';
import App from './App.vue';
import { applyTheme, loadThemePref } from './theme';

// Stamp the persisted theme choice on <html> before the app mounts (System =
// no attribute; the CSS prefers-color-scheme media query tracks the OS).
applyTheme(loadThemePref());

createApp(App).mount('#app');
