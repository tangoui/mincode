import { createApp, h } from 'vue';
import { ElScrollbar } from 'element-plus';
// Official builds swap this module for the stylesheets of the components the page uses.
import './element-plus-styles.js';
import '../styles/foundation/foundation.css';
// Plugin pages use the same control geometry, density and field treatment as the
// host: without these the shared controls render at Element's 32px default next
// to 28px host controls, with borders that ignore the neutral/accent palette and
// a control radius that resolves to 0 (square corners).
import '../styles/shared/element-plus-controls.css';
import '../styles/shared/compact-density.css';
import '../styles/shared/theme-inputs.css';
import './page.css';
import './scrollbar.css';
import { installSandboxForms } from './sandbox-forms.js';
export function mountPlugin(Page, config, options = {}) {
  installSandboxForms();
  const renderPage = () => h(Page, { config });
  createApp({
    render: options.pageScrollbar === false
      ? renderPage
      : () => h(ElScrollbar, { class: 'plugin-page-scrollbar' }, { default: renderPage }),
  }).mount('#app');
}
