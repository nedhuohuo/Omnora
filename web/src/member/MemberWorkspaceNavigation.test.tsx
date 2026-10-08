import { renderToStaticMarkup } from 'react-dom/server';
import { isValidElement, type ReactElement, type ReactNode } from 'react';
import { describe, expect, it, vi } from 'vitest';

import MemberWorkspaceNavigation from './MemberWorkspaceNavigation';

type NavigationButtonProps = {
  'data-nav-id': string;
  'data-active': boolean;
  onClick: () => void;
  children?: ReactNode;
};

function collectNavigationButtons(node: ReactNode): Array<ReactElement<NavigationButtonProps>> {
  if (Array.isArray(node)) return node.flatMap(collectNavigationButtons);
  if (!isValidElement(node)) return [];
  const element = node as ReactElement<{ children?: ReactNode }>;
  const children = Array.isArray(element.props.children)
    ? element.props.children.flatMap(collectNavigationButtons)
    : collectNavigationButtons(element.props.children);
  return element.type === 'button' ? [element as ReactElement<NavigationButtonProps>, ...children] : children;
}

describe('MemberWorkspaceNavigation', () => {
  const items = [
    { id: 'files', label: '文件' },
    { id: 'trash', label: '回收站' },
    { id: 'shares', label: '分享' },
    { id: 'tokens', label: 'AI Token' },
    { id: 'docs', label: '文档' },
    { id: 'account', label: '账户' },
  ] as const;

  it('renders one continuous account navigation list without personal or team-space groups', () => {
    const sidebar = renderToStaticMarkup(
      <MemberWorkspaceNavigation
        items={items}
        activeTab="files"
        onSelect={vi.fn()}
      />,
    );

    expect(sidebar).toContain('文件');
    expect(sidebar).toContain('回收站');
    expect(sidebar).toContain('分享');
    expect(sidebar).toContain('AI Token');
    expect(sidebar).toContain('文档');
    expect(sidebar).toContain('账户');
    expect(sidebar).not.toContain('个人空间');
    expect(sidebar).not.toContain('团队空间');
    expect(sidebar).not.toContain('共享空间');
    expect(sidebar).not.toContain('member-workspace-group');
    expect(sidebar).toContain('data-active="true"');
  });

  it('selects an account tab and marks it as the only active item', () => {
    const onSelect = vi.fn();
    const navigation = MemberWorkspaceNavigation({
      items,
      activeTab: 'shares',
      onSelect,
    });
    const buttons = collectNavigationButtons(navigation);
    const activeTabIds = buttons.filter((button) => button.props['data-active']).map((button) => button.props['data-nav-id']);
    const accountButton = buttons.find((button) => button.props['data-nav-id'] === 'account');

    expect(activeTabIds).toEqual(['shares']);
    expect(accountButton).toBeDefined();
    accountButton!.props.onClick();
    expect(onSelect).toHaveBeenCalledOnce();
    expect(onSelect).toHaveBeenCalledWith('account');
  });
});
