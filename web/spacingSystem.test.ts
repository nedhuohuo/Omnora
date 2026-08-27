import { readFileSync } from 'node:fs';

import { describe, expect, it } from 'vitest';

const stylesCss = readFileSync(new URL('./src/styles.css', import.meta.url), 'utf8');
const memberFilesCss = readFileSync(new URL('./src/member/member-files.css', import.meta.url), 'utf8');
const sharePortalCss = readFileSync(new URL('./src/share-portal.css', import.meta.url), 'utf8');
const sharePortalApp = readFileSync(new URL('./src/SharePortalApp.tsx', import.meta.url), 'utf8');

function ruleFor(css: string, selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = css.match(new RegExp(`${escaped}\\s*\\{[^}]*\\}`, 's'));

  if (!match) {
    throw new Error(`CSS rule not found for selector: ${selector}`);
  }

  return match[0];
}

describe('web spacing system tokens', () => {
  it.each([
    '--layout-page-inline: var(--space-7);',
    '--layout-page-block-start: var(--space-6);',
    '--layout-section-compact: var(--space-5);',
    '--layout-component: var(--space-4);',
    '--control-md: 36px;',
    '--control-lg: 40px;',
    '--table-header-height: 40px;',
    '--shell-topbar-height: 64px;',
    '--layout-content-bottom-reserve: 112px;',
  ])('defines the required token %s', (token) => {
    expect(stylesCss.includes(token), `styles.css should define ${token}`).toBe(true);
  });
});

describe('member files spacing cleanup', () => {
  it.each([
    ['.member-login-panel h1', 'margin: 4px 0 -12px'],
    ['.member-no-access', 'margin: 90px auto'],
    ['.member-readonly, .member-search-scope', 'padding: 9px 11px'],
    ['.member-path-suggest-empty', 'padding: 10px 11px'],
    ['.member-share-picker-list', 'gap: 2px'],
    ['.member-path-root', 'gap: 3px'],
    ['.member-route-next-request', 'margin-top: 6px'],
  ])('removes %s declaration %s', (selector, declaration) => {
    expect(ruleFor(memberFilesCss, selector)).not.toContain(declaration);
  });
});

describe('form action spacing', () => {
  it('keeps adjacent admin form actions separated', () => {
    expect(ruleFor(memberFilesCss, '.member-admin-form-actions')).toContain('gap: var(--layout-inline)');
  });
});

describe('collaboration section spacing', () => {
  it('uses explicit section spacing instead of browser heading margins', () => {
    expect(ruleFor(memberFilesCss, '.member-collaborations-directory > section + section')).toContain('margin-top: var(--layout-section)');
    expect(ruleFor(memberFilesCss, '.member-collaborations-directory .member-section-heading')).toContain('margin: 0 0 var(--layout-section-compact)');
    expect(ruleFor(memberFilesCss, '.member-collaborations-directory .member-section-heading h2')).toContain('margin: 0');
  });
});

describe('share portal style independence', () => {
  it('uses shared layout tokens without importing member page CSS', () => {
    expect(sharePortalApp).not.toContain("import './member/member-files.css';");
    expect(sharePortalCss).toContain('var(--shell-topbar-height)');
    expect(sharePortalCss).toContain('var(--layout-section)');
  });
});
