import js from '@eslint/js';
import react from 'eslint-plugin-react';
import tseslint from 'typescript-eslint';

// Two rules protect the offline and bilingual guarantees of the Portal (survival-portal.md
// sections 4.3 and 4.11): no text literal in JSX (every string is a translation key), and no
// style={{...}} (the CSP has no 'unsafe-inline'; use classes).
export default tseslint.config(
  { ignores: ['dist', 'node_modules', 'test-results', 'playwright-report'] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ['src/**/*.{ts,tsx}', 'e2e/**/*.{ts,mjs}'],
    languageOptions: { globals: { window: 'readonly', document: 'readonly', navigator: 'readonly', localStorage: 'readonly', fetch: 'readonly', URL: 'readonly', Headers: 'readonly', EventSource: 'readonly', KeyboardEvent: 'readonly', HTMLElement: 'readonly', HTMLDialogElement: 'readonly', RequestInit: 'readonly', Response: 'readonly', console: 'readonly', process: 'readonly' , setTimeout: 'readonly', clearTimeout: 'readonly' } },
  },
  {
    files: ['src/portal/**/*.tsx'],
    plugins: { react },
    rules: {
      'react/jsx-no-literals': ['error', { noStrings: false, allowedStrings: [] }],
      'no-restricted-syntax': [
        'error',
        {
          selector: "JSXAttribute[name.name='style']",
          message: 'No style attribute in the Portal: the CSP forbids inline styles. Use a CSS class.',
        },
      ],
    },
  },
);
