import { describe, expect, it } from 'vitest';

import { resumedUploadProgress, uploadStorageKey } from './uploadQueue';

describe('member upload queue', () => {
  it('keys resumable uploads by the content source and destination', () => {
    const file = { name: 'report.pdf', size: 100, lastModified: 42 };
    expect(uploadStorageKey('common_mount:mount-1', 'docs', file)).toContain('common_mount:mount-1.docs.report.pdf.100.42');
    expect(uploadStorageKey('common_mount:mount-1', 'docs', file)).not.toContain('space-1');
  });

  it('starts a resumed upload at the persisted part progress', () => {
    expect(resumedUploadProgress([{ number: 1, size: 32 }, { Number: 2, Size: 18 }], 100)).toBe(50);
  });
});
