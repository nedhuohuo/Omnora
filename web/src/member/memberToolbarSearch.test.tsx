import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';

import { MemberToolbarSearch } from './MemberStorageWorkspace';

describe('member toolbar search', () => {
  it('renders search input and action as one token-styled toolbar control', () => {
    const html = renderToStaticMarkup(
      <MemberToolbarSearch label="搜索" value="report" loading={false} onChange={() => undefined} onSearch={() => undefined} />,
    );

    expect(html).toContain('class="member-toolbar-search"');
    expect(html).toContain('role="search"');
    expect(html).toContain('aria-label="搜索"');
    expect(html).toContain('type="submit"');
  });
});
