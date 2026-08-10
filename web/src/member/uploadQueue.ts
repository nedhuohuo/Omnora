const uploadStoragePrefix = 'omnora.member.upload.';

export type UploadFileIdentity = Pick<File, 'name' | 'size' | 'lastModified'>;

export function uploadStorageKey(sourceKey: string, path: string, file: UploadFileIdentity) {
  return `${uploadStoragePrefix}${sourceKey}.${path}.${file.name}.${file.size}.${file.lastModified}`;
}

export function resumedUploadProgress(parts: Array<{ Number?: number; number?: number; Size?: number; size?: number }>, totalSize: number) {
  if (totalSize <= 0) return 0;
  const uploadedBytes = parts.reduce((total, part) => total + Number(part.Size ?? part.size ?? 0), 0);
  return Math.min(100, Math.round((uploadedBytes / totalSize) * 100));
}
