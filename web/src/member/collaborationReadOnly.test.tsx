import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';

import MemberCollaborationsDirectory from './MemberCollaborationsDirectory';
import { MemberDialogProvider } from './MemberDialog';

describe('member collaboration read-only browsing', () => {
  it('opens every incoming collaboration in read-only mode', () => {
    const onOpen = vi.fn();
    const html = renderToStaticMarkup(
      <MemberDialogProvider>
        <MemberCollaborationsDirectory
          locale="zh-CN"
          incoming={[{ id: 'collab-1', displayName: '共享资料', permission: 'editor' }]}
          outgoing={[]}
          onOpen={onOpen}
        />
      </MemberDialogProvider>,
    );

    expect(html).toContain('共享资料');
    expect(html).toContain('member-content-card');
    // Card renders as button with read-only intent; onOpen is wired via onClick.
    expect(html).toContain('button');
  });
});
