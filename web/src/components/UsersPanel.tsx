// SPDX-License-Identifier: AGPL-3.0-or-later

// Issue #82: multiple OTC "users" on one device, each a fully separate
// process/port/database/storage directory (see supervisor/supervisor.go
// on the backend). This panel only ever renders on the PRIMARY instance -
// every other user's own Settings page never even asks for the role, so
// it never shows this section at all (see the isPrimary check below).
import { useCallback, useEffect, useState } from "react";
import { useWS } from "../net/useWS";
import type { ReqEnvelope, RespEnvelope, User as PbUser } from "../proto/messages";
import "./SettingsForm.css";
import "./UsersPanel.css";

type Metrics = { storageMb: number; storagePct: number; activeConnections: number };

export default function UsersPanel() {
  const [isPrimary, setIsPrimary] = useState<boolean | null>(null);
  const [users, setUsers] = useState<PbUser[] | null>(null);
  const [status, setStatus] = useState<{ kind: "error" | "success"; text: string } | null>(null);

  const [newUsername, setNewUsername] = useState("");
  const [newPort, setNewPort] = useState("");
  const [creating, setCreating] = useState(false);

  const [deleteTarget, setDeleteTarget] = useState<PbUser | null>(null);
  const [deleteConfirmText, setDeleteConfirmText] = useState("");
  const [deleting, setDeleting] = useState(false);

  const [metrics, setMetrics] = useState<Record<string, Metrics>>({});
  const [busyActive, setBusyActive] = useState<string | null>(null);

  useEffect(() => {
    (async () => {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqGetInstanceRole", reqGetInstanceRole: {} };
      });
      setIsPrimary(resp.payload?.$case === "respInstanceRole" && resp.payload.respInstanceRole.isPrimary);
    })();
  }, []);

  const loadUsers = useCallback(async () => {
    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqListUsers", reqListUsers: {} };
    });
    if (resp.payload?.$case === "respUsers") {
      setUsers(resp.payload.respUsers.users);
    }
  }, []);

  useEffect(() => {
    if (isPrimary) void loadUsers();
  }, [isPrimary, loadUsers]);

  const fetchMetrics = async (uuid: string) => {
    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqGetUserMetrics", reqGetUserMetrics: { uuid } };
    });
    if (resp.payload?.$case === "respUserMetrics") {
      const m = resp.payload.respUserMetrics;
      setMetrics(prev => ({ ...prev, [uuid]: { storageMb: m.storageMb, storagePct: m.storagePct, activeConnections: m.activeConnections } }));
    }
  };

  const createUser = async () => {
    setCreating(true);
    setStatus(null);
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = {
          $case: "reqCreateUser",
          reqCreateUser: { username: newUsername.trim(), port: newPort ? parseInt(newPort, 10) : 0 },
        };
      });
      if (resp.payload?.$case === "respUsers") {
        setUsers(resp.payload.respUsers.users);
        setNewUsername("");
        setNewPort("");
        setStatus({ kind: "success", text: `User "${newUsername.trim()}" created.` });
      } else {
        setStatus({ kind: "error", text: resp.errorMessage || "Could not create user." });
      }
    } catch (err: any) {
      setStatus({ kind: "error", text: err?.message ?? String(err) });
    } finally {
      setCreating(false);
    }
  };

  const confirmDelete = async () => {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = {
          $case: "reqDeleteUser",
          reqDeleteUser: { uuid: deleteTarget.uuid, confirmUsername: deleteConfirmText },
        };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        setStatus({ kind: "success", text: `User "${deleteTarget.username}" deleted.` });
        setDeleteTarget(null);
        setDeleteConfirmText("");
        await loadUsers();
      } else {
        setStatus({ kind: "error", text: resp.errorMessage || "Could not delete user." });
      }
    } catch (err: any) {
      setStatus({ kind: "error", text: err?.message ?? String(err) });
    } finally {
      setDeleting(false);
    }
  };

  // Issue #90: reversible enable/disable, distinct from the permanent
  // Delete below - no type-to-confirm needed, since nothing is destroyed.
  const toggleActive = async (u: PbUser) => {
    setBusyActive(u.uuid);
    setStatus(null);
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqSetUserActive", reqSetUserActive: { uuid: u.uuid, active: !u.active } };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        await loadUsers();
      } else {
        setStatus({ kind: "error", text: resp.errorMessage || "Could not update this user." });
      }
    } catch (err: any) {
      setStatus({ kind: "error", text: err?.message ?? String(err) });
    } finally {
      setBusyActive(null);
    }
  };

  if (!isPrimary) return null;

  return (
    <section className="sf-section">
      <h3>Users</h3>
      <p className="sf-hint">
        Each user runs as its own fully separate instance — own port, own database, own storage —
        managed only from here.
      </p>

      {status && <div className={`sf-status ${status.kind}`}>{status.text}</div>}

      {users == null ? (
        <div className="sf-hint">Loading…</div>
      ) : (
        <ul className="up-list">
          {users.map(u => {
            const m = metrics[u.uuid];
            return (
              <li key={u.uuid} className="up-item">
                <div className="up-item-main">
                  <div className="up-item-name">
                    {u.username}
                    {!u.active && <span className="up-badge up-badge-muted">inactive</span>}
                  </div>
                  <div className="up-item-sub">port {u.port} · {u.subdomain}</div>
                </div>
                <div className="up-item-metrics">
                  {m ? (
                    <span className="sf-hint" style={{ margin: 0 }}>
                      {m.storageMb.toFixed(0)} MB ({m.storagePct.toFixed(1)}%) · {m.activeConnections} active
                    </span>
                  ) : (
                    <button className="sf-btn small" onClick={() => void fetchMetrics(u.uuid)}>Load usage</button>
                  )}
                </div>
                <div className="up-item-actions">
                  <button
                    className="sf-btn small sf-btn-secondary"
                    disabled={busyActive === u.uuid}
                    onClick={() => void toggleActive(u)}
                  >
                    {busyActive === u.uuid ? "…" : u.active ? "Disable" : "Enable"}
                  </button>
                  <button className="sf-btn small sf-danger" onClick={() => { setDeleteTarget(u); setDeleteConfirmText(""); }}>
                    Delete
                  </button>
                </div>
              </li>
            );
          })}
          {users.length === 0 && <li className="sf-hint">No additional users yet.</li>}
        </ul>
      )}

      <div className="up-add-row">
        <input
          className="sf-input"
          placeholder="username"
          value={newUsername}
          onChange={(e) => setNewUsername(e.target.value)}
          autoCapitalize="none"
          autoCorrect="off"
        />
        <input
          className="sf-input up-port-input"
          placeholder="port (optional)"
          inputMode="numeric"
          value={newPort}
          onChange={(e) => setNewPort(e.target.value.replace(/\D/g, ""))}
        />
        <button
          className="sf-btn"
          disabled={creating || !/^[a-z0-9-]+$/.test(newUsername.trim())}
          onClick={() => void createUser()}
        >
          {creating ? "Creating…" : "Add User"}
        </button>
      </div>

      {deleteTarget && (
        <div className="sf-modal" onClick={() => setDeleteTarget(null)}>
          <div className="sf-modal-inner" onClick={e => e.stopPropagation()}>
            <p>
              Delete <strong>{deleteTarget.username}</strong>? This permanently removes its database
              and every file it stored. Type <strong>{deleteTarget.username}</strong> to confirm.
            </p>
            <input
              className="sf-input"
              autoFocus
              value={deleteConfirmText}
              onChange={(e) => setDeleteConfirmText(e.target.value)}
              autoCapitalize="none"
              autoCorrect="off"
            />
            <div className="sf-modal-actions">
              <button className="sf-btn small" onClick={() => setDeleteTarget(null)}>Cancel</button>
              <button
                className="sf-btn small sf-danger"
                disabled={deleteConfirmText !== deleteTarget.username || deleting}
                onClick={() => void confirmDelete()}
              >
                {deleting ? "Deleting…" : "Delete"}
              </button>
            </div>
          </div>
        </div>
      )}
    </section>
  );
}
