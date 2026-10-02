// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The Files grid's icon for a file without a thumbnail: a generic document
// with a coloured, labelled band - PDF red, documents blue, spreadsheets
// green, and so on - in the colours people know those types by, without
// any vendor's logo. The same mapping is in the apps (FileTypeIcon.swift,
// FileTypeIcon.kt): change all three together.

type Badge = { label: string; color: string };

const groups: Array<[string[], string | null, string]> = [
  [["pdf"], "PDF", "#E5252A"],
  [["doc", "docx", "odt", "rtf", "pages"], "DOC", "#2B579A"],
  [["xls", "xlsx", "ods", "csv", "numbers"], "XLS", "#217346"],
  [["ppt", "pptx", "odp", "key"], "PPT", "#D24726"],
  [["txt", "md", "log"], "TXT", "#6B7280"],
  [["zip", "rar", "7z", "tar", "gz", "tgz", "bz2", "xz"], "ZIP", "#B7791F"],
  [["mp3", "wav", "flac", "m4a", "aac", "ogg", "opus"], null, "#7C3AED"],
  [["json", "js", "ts", "go", "py", "swift", "kt", "java", "c", "cpp", "h", "html", "css", "xml", "yml", "yaml", "sh"], "</>", "#0F766E"],
  [["apk"], "APK", "#3DDC84"],
  [["jpg", "jpeg", "png", "heic", "gif", "webp", "tiff", "bmp", "dng", "raw", "cr2", "nef", "arw"], null, "#0EA5E9"],
  [["mp4", "mov", "m4v", "mkv", "avi", "webm", "3gp"], null, "#DB2777"],
];

/** The label and colour a file's icon gets, from its name's extension. */
export function fileTypeBadge(name: string): Badge {
  const dot = name.lastIndexOf(".");
  const ext = dot > 0 ? name.slice(dot + 1).toLowerCase() : "";
  for (const [exts, label, color] of groups) {
    if (exts.includes(ext)) return { label: label ?? ext.toUpperCase().slice(0, 4), color };
  }
  return { label: ext ? ext.toUpperCase().slice(0, 4) : "FILE", color: "#64748B" };
}

export default function FileTypeIcon({ name, size = 64 }: { name: string; size?: number }) {
  const { label, color } = fileTypeBadge(name);
  const fontSize = label.length > 3 ? 13 : 16;
  return (
    <svg width={size * 0.78} height={size} viewBox="0 0 78 100" role="img" aria-label={`${label} file`} className="fti">
      {/* The page, with its top-right corner folded. */}
      <path d="M8 2h44l24 24v64a8 8 0 0 1-8 8H8a8 8 0 0 1-8-8V10a8 8 0 0 1 8-8z" className="fti-page" />
      <path d="M52 2v16a8 8 0 0 0 8 8h16z" className="fti-fold" />
      <rect x="0" y="56" width="78" height="30" fill={color} />
      <text x="39" y="76.5" textAnchor="middle" fontSize={fontSize} fontWeight="800"
        fontFamily="system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif" fill="#fff">{label}</text>
    </svg>
  );
}
