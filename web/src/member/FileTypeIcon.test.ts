import { describe, expect, it } from 'vitest';
import { fileIconType } from './FileTypeIcon';

describe('fileIconType', () => {
  it('maps directories to the folder icon regardless of the name', () => {
    expect(fileIconType('dir', 'anything')).toBe('dir');
    expect(fileIconType('dir', 'photos.v2')).toBe('dir');
    expect(fileIconType('dir', '.hidden')).toBe('dir');
  });

  it('maps common image extensions (case-insensitive)', () => {
    expect(fileIconType('file', 'photo.jpg')).toBe('image');
    expect(fileIconType('file', 'photo.JPEG')).toBe('image');
    expect(fileIconType('file', 'scan.png')).toBe('image');
    expect(fileIconType('file', 'anim.webp')).toBe('image');
    expect(fileIconType('file', 'logo.svg')).toBe('image');
  });

  it('maps media extensions', () => {
    expect(fileIconType('file', 'movie.mp4')).toBe('video');
    expect(fileIconType('file', 'clip.mov')).toBe('video');
    expect(fileIconType('file', 'song.mp3')).toBe('audio');
    expect(fileIconType('file', 'voice.wav')).toBe('audio');
  });

  it('maps document extensions', () => {
    expect(fileIconType('file', 'manual.pdf')).toBe('doc');
    expect(fileIconType('file', 'report.docx')).toBe('doc');
    expect(fileIconType('file', 'README.md')).toBe('doc');
    expect(fileIconType('file', 'notes.txt')).toBe('doc');
  });

  it('maps archives and code', () => {
    expect(fileIconType('file', 'backup.zip')).toBe('archive');
    expect(fileIconType('file', 'bundle.tar.gz')).toBe('archive');
    expect(fileIconType('file', 'app.tsx')).toBe('code');
    expect(fileIconType('file', 'config.json')).toBe('code');
    expect(fileIconType('file', 'deploy.sh')).toBe('code');
  });

  it('falls back to the generic file icon for unknown or missing extensions', () => {
    expect(fileIconType('file', 'report.final')).toBe('file');
    expect(fileIconType('file', 'README')).toBe('file');
    expect(fileIconType('file', 'noext.')).toBe('file');
    expect(fileIconType('file', '.gitignore')).toBe('file');
  });
});
