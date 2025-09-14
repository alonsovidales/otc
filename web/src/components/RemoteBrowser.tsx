import React, { useCallback, useEffect, useState } from "react";
import type { ReqEnvelope, RespEnvelope, File as MsgFile, ListOfFiles } from "../proto/messages";
import { File as PbFile } from "../proto/messages";
import { useWS } from "../net/useWS";

type CtxMenuState = { open: boolean; x: number; y: number };

/** Heuristics to detect directories (proto has no explicit isDir) */
function isDir(f: MsgFile): boolean {
  return f.mime === "inode/directory" || f.path.endsWith("/");
}

/** Path helpers */
function joinPath(dir: string, name: string) {
  return (dir.replace(/\/+$/, "") + "/" + name).replace(/\/+/g, "/");
}
function baseName(p: string) {
  const t = p.replace(/\/+$/, "");
  const i = t.lastIndexOf("/");
  return i >= 0 ? t.slice(i + 1) : t;
}
function formatBytes(n?: number | bigint): string {
  if (n == null) return "";
  const num = typeof n === "bigint" ? Number(n) : n;
  if (num < 1024) return `${num} B`;
  const u = ["KB", "MB", "GB", "TB"];
  let v = num;
  let i = -1;
  do { v /= 1024; i++; } while (v >= 1024 && i < u.length - 1);
  return `${v.toFixed(1)} ${u[i]}`;
}
function asDate(ts: any): Date | undefined {
  if (!ts) return undefined;

  if (ts instanceof Date) {
    return ts; // already a Date
  }

  if (typeof ts.seconds !== "undefined") {
    const s =
      typeof ts.seconds === "bigint"
        ? Number(ts.seconds)
        : typeof ts.seconds === "string"
        ? parseInt(ts.seconds, 10)
        : ts.seconds;
    return new Date(s * 1000 + (ts.nanos ?? 0) / 1e6);
  }

  return undefined;
}

export default function RemoteBrowser() {
  console.log('Use WS RemoteBrowser-', useWS.connected());

  const [cwd, setCwd] = useState<string>("/");               // current directory
  const [rows, setRows] = useState<MsgFile[]>([]);           // current listing
  const [loading, setLoading] = useState<boolean>(false);
  const [error, setError] = useState<string | null>(null);
  const [totalFiles, setTotalFiles] = useState<number>(0);

  const [selected, setSelected] = useState<Set<string>>(new Set()); // selected by path
  const [ctxMenu, setCtxMenu] = useState<CtxMenuState>({ open: false, x: 0, y: 0 });

  // ------- WS helpers (oneof=$case) -------
  const listDir = useCallback(async (path: string) => {
    setLoading(true); setError(null);
    try {
      console.log('Requesting files...');
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as Partial<ReqEnvelope>).payload = { $case: "reqListFiles", reqListFiles: { path } };
      });
      console.log('Response:', resp);
      const p = (resp as any).payload;
      if (p?.$case === "respListOfFiles") {
        let list: ListOfFiles = p.respListOfFiles;
        console.log('Total FileS:', list.files.length);
        setTotalFiles(list.files.length);
        if (path != '/') {
          list.files = [PbFile.fromPartial({path: "..", mime: "inode/directory"}), ...list.files];
        }
        setRows((list.files || []).slice().sort((a, b) => {
          const ad = isDir(a) ? 0 : 1;
          const bd = isDir(b) ? 0 : 1;
          return ad - bd || baseName(a.path).localeCompare(baseName(b.path));
        }));
      } else if (resp.error) {
        setError(resp.errorMessage ?? "List error");
      } else {
        setRows([]);
      }
    } catch (err: any) {
      setError(err.message || String(err));
      setRows([]);
    } finally {
      setLoading(false);
      setSelected(new Set());
    }
  }, [useWS.request]);

  const getFile = useCallback(async (path: string): Promise<MsgFile | null> => {
    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqGetFile", reqGetFile: { path } };
    });
    const p = (resp as any).payload;
    if (p?.$case === "respFile") return p.respFile as MsgFile;
    return null;
  }, [useWS.request]);

  const delFile = useCallback(async (path: string) => {
    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqDelFile", reqDelFile: { path } };
    });
    if ((resp as any).payload?.$case === "respAck" && (resp as any).payload.respAck?.ok) return true;
    return !resp.error;
  }, [useWS.request]);

  const uploadFile = useCallback(async (targetDir: string, file: File, forceOverride = false) => {
    const targetPath = joinPath(targetDir, file.name);
    const content = new Uint8Array(await file.arrayBuffer());
    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqUploadFile", reqUploadFile: { path: targetPath, content, forceOverride } };
    });
    if ((resp as any).payload?.$case === "respAck" && (resp as any).payload.respAck?.ok) return true;
    if (resp.error) throw new Error(resp.errorMessage || "Upload failed");
    return true;
  }, [useWS.request]);

  // ------- initial + on cwd change -------
  useEffect(() => { if (useWS.connected()) listDir(cwd); }, [useWS.connected(), cwd, listDir]);

  // ------- UI handlers -------
  const onRowClick = async (f: MsgFile, ev: React.MouseEvent) => {
    if (ev.shiftKey || ev.metaKey || ev.ctrlKey) {
      // toggle selection
      setSelected(prev => {
        const next = new Set(prev);
        if (next.has(f.path)) next.delete(f.path); else next.add(f.path);
        return next;
      });
      return;
    }
    if (isDir(f)) {
      if (f.path == '..') {
        setCwd(cwd.slice(0, cwd.lastIndexOf("/", cwd.length - 2) + 1));
      } else {
        setCwd(f.path.replace(/\/+$/, "") + "/");
        setCtxMenu({ open: false, x: 0, y: 0 });
      }
    } else {
      // Download file
      const got = await getFile(f.path);
      const bytes: Uint8Array | undefined = (got as any)?.content;
      if (!bytes || bytes.length === 0) {
        alert("Server did not return content; implement chunked download or ensure resp_file.content is set.");
        return;
      }
      const blob = new Blob([bytes], { type: f.mime || "application/octet-stream" });
      const a = document.createElement("a");
      a.href = URL.createObjectURL(blob);
      a.download = baseName(f.path);
      document.body.appendChild(a);
      a.click();
      a.remove();
      URL.revokeObjectURL(a.href);
    }
  };

  const onHeaderCheckbox = (ev: React.ChangeEvent<HTMLInputElement>) => {
    if (ev.target.checked) setSelected(new Set(rows.map(r => r.path)));
    else setSelected(new Set());
  };
  const onRowCheckbox = (f: MsgFile, checked: boolean) => {
    setSelected(prev => {
      const next = new Set(prev);
      if (checked) next.add(f.path); else next.delete(f.path);
      return next;
    });
  };

  // drag & drop upload to current dir
  const onDrop = async (ev: React.DragEvent) => {
    ev.preventDefault();
    const files = Array.from(ev.dataTransfer.files || []);
    if (files.length === 0) return;
    setLoading(true);
    try {
      for (const file of files) {
        await uploadFile(cwd, file);
      }
      await listDir(cwd);
    } catch (e: any) {
      setError(e.message || String(e));
    } finally {
      setLoading(false);
    }
  };

  // context menu
  const openCtx = (f: MsgFile, ev: React.MouseEvent) => {
    ev.preventDefault();
    setCtxMenu({ open: true, x: ev.clientX, y: ev.clientY });
    // if right-click on an unselected item, select only that
    setSelected(prev => (prev.has(f.path) ? prev : new Set([f.path])));
  };
  const closeCtx = () => setCtxMenu({ open: false, x: 0, y: 0 });

  // actions
  const doDelete = async () => {
    const paths = Array.from(selected);
    if (paths.length === 0) return;
    if (!confirm(`Delete ${paths.length} item(s)?`)) return;
    for (const p of paths) await delFile(p);
    closeCtx();
    await listDir(cwd);
  };

  const doRename = async () => {
    const paths = Array.from(selected);
    if (paths.length !== 1) { alert("Select exactly one file to rename."); return; }
    const oldPath = paths[0];
    const f = rows.find(r => r.path === oldPath);
    if (!f) return;
    if (isDir(f)) { alert("Renaming directories is not supported with current proto."); return; }
    const newName = prompt("New name:", baseName(oldPath));
    if (!newName || newName === baseName(oldPath)) return;

    // client-side rename: GetFile -> UploadFile(new) -> DelFile(old)
    const got = await getFile(oldPath);
    const bytes: Uint8Array | undefined = (got as any)?.content;
    if (!bytes || bytes.length === 0) { alert("Server did not return file content for rename."); return; }

    const newPath = joinPath(cwd, newName);
    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqUploadFile", reqUploadFile: { path: newPath, content: bytes, forceOverride: false } };
    });
    if ((resp as any).payload?.$case === "respAck" && (resp as any).payload.respAck?.ok) {
      await delFile(oldPath);
      await listDir(cwd);
    } else {
      alert("Rename failed: upload step");
    }
    closeCtx();
  };

  const doShare = async () => {
    const paths = Array.from(selected);
    if (paths.length === 0) return;
    // Placeholder: copy NAS paths to clipboard (real share needs server support)
    await navigator.clipboard.writeText(paths.join("\n"));
    closeCtx();
    alert("Paths copied to clipboard. Implement server-side share links if desired.");
  };

  // --------- render ---------
  return (
    <div
      className="remote-wrap"
      onDragOver={(e) => e.preventDefault()}
      onDrop={onDrop}
      onClick={() => ctxMenu.open && closeCtx()}
      style={{ position: "relative" }}
    >
      <div className="toolbar" style={{ display: "flex", gap: 8, alignItems: "center", marginBottom: 8 }}>
        <a onClick={() => listDir(cwd)}>⟳</a>
        <strong>Path:</strong>
        <code>{cwd}</code>
        <span style={{ marginLeft: "auto" }}>
          <strong>Total files: </strong>
          <code>{totalFiles}</code>
        </span>
      </div>

      {error && <div style={{ color: "#dc2626", marginBottom: 8 }}>{error}</div>}

      <div className="table" style={{ border: "1px solid #e5e7eb", borderRadius: 8, overflow: "hidden", width: "100%", height: "100%" }}>
        <div className="thead" style={{ display: "grid", gridTemplateColumns: "40px 1fr 160px 170px 160px", background: "#334145", borderBottom: "1px solid #e5e7eb", padding: "8px 12px", fontWeight: 600 }}>
          <div><input type="checkbox" onChange={onHeaderCheckbox} checked={selected.size > 0 && selected.size === rows.length} aria-label="Select all" /></div>
          <div>Name</div>
          <div>Size</div>
          <div>Created</div>
          <div>Modified</div>
        </div>

        <div className="tbody" style={{ maxHeight: 480, overflow: "auto", color: "#6b7280" }}>
          {loading ? (
            <div style={{ padding: 16 }}>Loading…</div>
          ) : rows.length === 0 ? (
            <div style={{ padding: 16, color: "#6b7280" }}>Empty</div>
          ) : (
            rows.map((f) => {
              const dir = isDir(f);
              const checked = selected.has(f.path);
              const created = asDate(f.created);
              const modified = asDate(f.modified);
              return (
                <div
                  key={f.path}
                  className="tr"
                  onDoubleClick={(e) => onRowClick(f, e)}
                  onClick={(e) => onRowClick(f, e)}
                  onContextMenu={(e) => openCtx(f, e)}
                  style={{
                    display: "grid",
                    gridTemplateColumns: "40px 1fr 160px 170px 160px",
                    padding: "8px 12px",
                    borderBottom: "1px solid #f3f4f6",
                    cursor: "default",
                    background: checked ? "#eef2ff" : "#fafafa",
                  }}
                >
                  <div>
                    <input
                      type="checkbox"
                      checked={checked}
                      onChange={(ev) => onRowCheckbox(f, ev.target.checked)}
                      onClick={(ev) => ev.stopPropagation()}
                      aria-label={`Select ${baseName(f.path)}`}
                    />
                  </div>
                  <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                    <span style={{ width: 18 }}>{dir ? "📁" : "📄"}</span>
                    <span style={{ color: dir ? "#1d4ed8" : "#111827" }}>{baseName(f.path) || "/"}</span>
                  </div>
                  <div>{dir ? "—" : formatBytes(f.size)}</div>
                  <div>{created ? created.toLocaleString() : ""}</div>
                  <div>{modified ? modified.toLocaleString() : ""}</div>
                </div>
              );
            })
          )}
        </div>
      </div>

      {/* Context Menu */}
      {ctxMenu.open && (
        <div
          style={{
            position: "fixed",
            top: ctxMenu.y,
            left: ctxMenu.x,
            background: "#fff",
            border: "1px solid #e5e7eb",
            borderRadius: 8,
            boxShadow: "0 10px 20px rgba(0,0,0,0.08)",
            zIndex: 1000,
            minWidth: 160,
            padding: 6
          }}
          onClick={(e) => e.stopPropagation()}
        >
          <button onClick={doRename} style={cmBtn}>Rename</button>
          <button onClick={doDelete} style={cmBtn}>Delete</button>
          <button onClick={doShare}  style={cmBtn}>Share</button>
        </div>
      )}

      <div style={{ marginTop: 8, color: "#6b7280" }}>
        Tip: Drag files anywhere in the table to upload to <code>{cwd}</code>.  
        Double-click a folder to open it. Right-click for actions.
      </div>
    </div>
  );
}

const cmBtn: React.CSSProperties = {
  display: "block",
  width: "100%",
  textAlign: "left",
  background: "transparent",
  border: "none",
  padding: "8px 10px",
  borderRadius: 6,
  cursor: "pointer",
};

