const OPAQUE_ID_PATTERN = /^(spc|mnt|ait|shr|usr|acc|ses|upl|bkp|job)_[A-Za-z0-9_-]+$/;
const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function readableLabel(value: string | undefined) {
  const trimmed = value?.trim();
  if (!trimmed || OPAQUE_ID_PATTERN.test(trimmed) || UUID_PATTERN.test(trimmed)) return '';
  return trimmed;
}

export function joinReadableLabels(values: Array<string | undefined>) {
  return values.map(readableLabel).filter(Boolean).join(' / ');
}
