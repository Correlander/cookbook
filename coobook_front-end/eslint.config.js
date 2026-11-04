import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'
import { defineConfig, globalIgnores } from 'eslint/config'

export default defineConfig([
  globalIgnores(['dist', '.astro']),
  {
    files: ['**/*.{js,jsx}'],
    extends: [
      js.configs.recommended,
      reactRefresh.configs.vite,
    ],
    plugins: {
      'react-hooks': reactHooks,
    },
    languageOptions: {
      ecmaVersion: 2020,
      globals: globals.browser,
      parserOptions: {
        ecmaVersion: 'latest',
        ecmaFeatures: { jsx: true },
        sourceType: 'module',
      },
    },
    rules: {
      'no-unused-vars': ['error', { varsIgnorePattern: '^[A-Z_]' }],
      'react-hooks/rules-of-hooks': 'error',
      'react-hooks/exhaustive-deps': 'warn',
    },
  },
  {
    files: ['**/*.{ts,tsx}'],
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    plugins: { 'react-hooks': reactHooks },
    languageOptions: {
      globals: globals.browser,
      parserOptions: { ecmaFeatures: { jsx: true }, sourceType: 'module' },
    },
    rules: {
      'no-unused-vars': 'off',
      '@typescript-eslint/no-unused-vars': ['error', { varsIgnorePattern: '^[A-Z_]' }],
      'react-hooks/rules-of-hooks': 'error',
      'react-hooks/exhaustive-deps': 'warn',
    },
  },
  {
    files: ['src/**/*.{js,jsx,ts,tsx}'],
    rules: {
      'no-restricted-imports': ['error', {
        patterns: [{
          group: ['react-router', 'react-router/*', 'react-router-dom', 'react-router-dom/*'],
          message: 'Use Astro document routes and ordinary links for page navigation.',
        }],
      }],
    },
  },
  {
    files: ['src/app/*Root.tsx'],
    ignores: ['src/app/AppRoot.tsx'],
    rules: {
      'no-restricted-imports': ['error', {
        patterns: [
          {
            group: ['react-router', 'react-router/*', 'react-router-dom', 'react-router-dom/*'],
            message: 'Use Astro document routes and ordinary links for page navigation.',
          },
          {
            group: ['../App', '../App.*'],
            message: 'Route roots must own their workflow; only AppRoot may mount Home App.',
          },
        ],
      }],
    },
  },
])
