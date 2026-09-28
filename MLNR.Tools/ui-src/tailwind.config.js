/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{ts,js,html}'],
  darkMode: ['class', '[data-theme="dark"]'],
  theme: {
    extend: {
      colors: {
        // 主色
        primary: {
          DEFAULT: 'rgb(var(--c-primary) / <alpha-value>)',
          hover: 'rgb(var(--c-primary-hover) / <alpha-value>)',
          soft: 'rgb(var(--c-primary-soft) / <alpha-value>)',
          'soft-text': 'rgb(var(--c-primary-soft-text) / <alpha-value>)',
        },
        // 成功
        success: {
          DEFAULT: 'rgb(var(--c-success) / <alpha-value>)',
          soft: 'rgb(var(--c-success-soft) / <alpha-value>)',
          'soft-text': 'rgb(var(--c-success-soft-text) / <alpha-value>)',
        },
        // 危险
        danger: {
          DEFAULT: 'rgb(var(--c-danger) / <alpha-value>)',
          hover: 'rgb(var(--c-danger-hover) / <alpha-value>)',
          soft: 'rgb(var(--c-danger-soft) / <alpha-value>)',
          'soft-text': 'rgb(var(--c-danger-soft-text) / <alpha-value>)',
        },
        // 警告
        warning: {
          DEFAULT: 'rgb(var(--c-warning) / <alpha-value>)',
          soft: 'rgb(var(--c-warning-soft) / <alpha-value>)',
          'soft-text': 'rgb(var(--c-warning-soft-text) / <alpha-value>)',
        },
        // 强调（转码中等）
        accent: {
          DEFAULT: 'rgb(var(--c-accent) / <alpha-value>)',
          soft: 'rgb(var(--c-accent-soft) / <alpha-value>)',
          'soft-text': 'rgb(var(--c-accent-soft-text) / <alpha-value>)',
        },
        // 下载中
        download: {
          DEFAULT: 'rgb(var(--c-download) / <alpha-value>)',
          soft: 'rgb(var(--c-download-soft) / <alpha-value>)',
          'soft-text': 'rgb(var(--c-download-soft-text) / <alpha-value>)',
        },
        // 中性状态
        neutral: {
          DEFAULT: 'rgb(var(--c-neutral) / <alpha-value>)',
          soft: 'rgb(var(--c-neutral-soft) / <alpha-value>)',
          'soft-text': 'rgb(var(--c-neutral-soft-text) / <alpha-value>)',
        },
        // 表面 / 背景
        page: 'rgb(var(--c-page) / <alpha-value>)',
        surface: {
          DEFAULT: 'rgb(var(--c-surface) / <alpha-value>)',
          hover: 'rgb(var(--c-surface-hover) / <alpha-value>)',
          alt: 'rgb(var(--c-surface-alt) / <alpha-value>)',
        },
        line: {
          DEFAULT: 'rgb(var(--c-line) / <alpha-value>)',
          subtle: 'rgb(var(--c-line-subtle) / <alpha-value>)',
        },
        // 文字
        ink: {
          DEFAULT: 'rgb(var(--c-ink) / <alpha-value>)',
          muted: 'rgb(var(--c-ink-muted) / <alpha-value>)',
          subtle: 'rgb(var(--c-ink-subtle) / <alpha-value>)',
        },
      },
    },
  },
  plugins: [],
}
