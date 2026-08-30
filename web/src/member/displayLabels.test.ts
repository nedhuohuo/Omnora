import { describe, expect, it } from 'vitest';
import { spaceDisplayName } from './displayLabels';

describe('spaceDisplayName', () => {
  it('localizes personal spaces without exposing the stored account-derived name', () => {
    expect(spaceDisplayName({ type: 'personal', name: "nedhuo@gmail.com's space" }, '我的空间')).toBe('我的空间');
    expect(spaceDisplayName({ type: 'personal', name: 'My Space' }, 'My Space')).toBe('My Space');
  });

  it('preserves shared space names', () => {
    expect(spaceDisplayName({ type: 'shared', name: '家庭共享' }, '我的空间')).toBe('家庭共享');
  });
});
