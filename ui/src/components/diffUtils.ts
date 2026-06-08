const buildUnifiedDiff = (
  path: string,
  oldStr: string,
  newStr: string,
): string => {
  const oldLines = oldStr.split('\n');
  const newLines = newStr.split('\n');
  const header = `--- ${path}\toriginal\n+++ ${path}\tmodified\n@@ -1,${oldLines.length} +1,${newLines.length} @@\n`;
  const body =
    oldLines.map((l) => `-${l}`).join('\n') +
    '\n' +
    newLines.map((l) => `+${l}`).join('\n');
  return header + body;
};

export const extractDiff = (
  value: unknown,
): { diff: string; path?: string } | null => {
  if (value == null || typeof value !== 'object') return null;
  const obj = value as Record<string, unknown>;

  if (typeof obj.diff === 'string') {
    const path =
      typeof obj.path === 'string'
        ? obj.path
        : typeof obj.file_path === 'string'
          ? (obj.file_path as string)
          : undefined;
    return { diff: obj.diff, path };
  }

  const path =
    typeof obj.path === 'string'
      ? obj.path
      : typeof obj.file_path === 'string'
        ? (obj.file_path as string)
        : undefined;
  const oldStr =
    typeof obj.old_str === 'string'
      ? obj.old_str
      : typeof obj.old_string === 'string'
        ? (obj.old_string as string)
        : undefined;
  const newStr =
    typeof obj.new_str === 'string'
      ? obj.new_str
      : typeof obj.new_string === 'string'
        ? (obj.new_string as string)
        : undefined;
  if (path && oldStr !== undefined && newStr !== undefined) {
    return { diff: buildUnifiedDiff(path, oldStr, newStr), path };
  }

  if (path && typeof obj.content === 'string') {
    const lines = (obj.content as string).split('\n');
    const diff =
      `--- ${path}\t(new file)\n+++ ${path}\tcreated\n@@ -0,0 +1,${lines.length} @@\n` +
      lines.map((l) => `+${l}`).join('\n');
    return { diff, path };
  }

  return null;
};
