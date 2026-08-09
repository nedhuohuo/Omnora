import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import AdminIndexJobsPanel from './AdminIndexJobsPanel';
import AdminMountsPanel from './AdminMountsPanel';

 describe('admin mount panel', () => {
  it('renders account-level mount controls without Space or managed mount choices', () => {
    const html = renderToStaticMarkup(<AdminMountsPanel locale="zh-CN" isInitialAdmin={false} />);
    expect(html).toContain('挂载管理');
    expect(html).toContain('外部目录');
    expect(html).not.toContain('空间');
    expect(html).not.toContain('托管挂载');
    expect(html).not.toContain('受限挂载');
  });

  it('renders index targets as mounts without Space labels', () => {
    const html = renderToStaticMarkup(<AdminIndexJobsPanel locale="zh-CN" />);
    expect(html).toContain('索引任务');
    expect(html).toContain('挂载');
    expect(html).not.toContain('空间');
  });

  it('shows restricted governance only to the initial administrator', () => {
    const ordinary = renderToStaticMarkup(<AdminMountsPanel locale="zh-CN" isInitialAdmin={false} />);
    const initial = renderToStaticMarkup(<AdminMountsPanel locale="zh-CN" isInitialAdmin />);
    expect(ordinary).not.toContain('受限挂载');
    expect(initial).toContain('受限挂载');
  });
});
