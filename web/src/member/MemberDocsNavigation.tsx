import type { DocsTab } from './MemberDocsPanel';
import { type MemberLocale, localeMessages } from './i18n';

type LocaleText = (typeof localeMessages)[MemberLocale];

type DocsNavigationProps = {
  locale: MemberLocale;
  tab: DocsTab;
  onTabChange: (tab: DocsTab) => void;
};

const DOCS_NAV_ITEMS: Array<{ tab: DocsTab; label: keyof LocaleText }> = [
  { tab: 'mcp', label: 'docsNavMcp' },
  { tab: 'openapi', label: 'docsNavOpenapi' },
  { tab: 'faq', label: 'docsNavFaq' },
];

export default function MemberDocsNavigation({ locale, tab, onTabChange }: DocsNavigationProps) {
  const text = localeMessages[locale];

  return (
    <div className="member-docs-navigation">
      <nav className="member-docs-directory" aria-label={text.docsNavLabel}>
        {DOCS_NAV_ITEMS.map((item) => (
          <button
            key={item.tab}
            type="button"
            className="member-docs-directory-item"
            aria-current={tab === item.tab ? 'page' : undefined}
            onClick={() => onTabChange(item.tab)}
          >
            {text[item.label]}
          </button>
        ))}
      </nav>
      <label className="member-docs-select-wrap">
        <span>{text.docsNavLabel}</span>
        <select value={tab} onChange={(event) => onTabChange(event.target.value as DocsTab)}>
          {DOCS_NAV_ITEMS.map((item) => (
            <option key={item.tab} value={item.tab}>{text[item.label]}</option>
          ))}
        </select>
      </label>
    </div>
  );
}
