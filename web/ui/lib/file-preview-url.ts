// Only server-issued local preview grants may become navigable frame/link URLs.
export function filePreviewURL(value: string | undefined): string | undefined {
  if (!value || !/^\/api\/media\/[a-f0-9]{64}(?:\/[^/?#]+)?$/.test(value)) return undefined;
  const leaf = value.split('/')[4];
  if (leaf) {
    try {
      const decoded = decodeURIComponent(leaf);
      if (decoded.startsWith('.') || /[\\/\x00-\x1f:]/.test(decoded)) return undefined;
    } catch { return undefined; }
  }
  return value;
}
