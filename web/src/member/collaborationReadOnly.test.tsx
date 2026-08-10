import { Children, isValidElement, type ReactElement, type ReactNode } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';

import MemberCollaborationsDirectory from './MemberCollaborationsDirectory';

describe('member collaboration read-only browsing', () => {
  it('opens every incoming collaboration in read-only mode', () => {
    const onOpen = vi.fn();
    const tree = MemberCollaborationsDirectory({
      locale: 'zh-CN',
      incoming: [{ id: 'collab-1', displayName: '共享资料', permission: 'editor' }],
      outgoing: [],
      onOpen,
    });
    const openButton = findButton(tree, '共享资料');

    expect(openButton).toBeDefined();
    openButton?.props.onClick?.();
    expect(onOpen).toHaveBeenCalledWith(
      { source: 'collaboration', collaborationId: 'collab-1', path: '.' },
      '共享资料',
      true,
    );
  });
});

function findButton(node: ReactNode, label: string): ReactElement<{ children?: ReactNode; onClick?: () => void }> | undefined {
  if (!isValidElement(node)) return undefined;
  const element = node as ReactElement<{ children?: ReactNode; onClick?: () => void }>;
  if (element.type === 'button' && renderToStaticMarkup(element).includes(label)) return element;
  for (const child of Children.toArray(element.props.children)) {
    const match = findButton(child, label);
    if (match) return match;
  }
  return undefined;
}
