// SPDX-License-Identifier: AGPL-3.0-or-later

// Issue #82: multiple OTC "users" on one device, each a fully separate
// process/port/database/storage directory (see supervisor/supervisor.go
// on the backend). This panel only ever renders on the PRIMARY instance -
// every other user's own Settings page never even asks for the role, so
// it never shows this section at all (see the isPrimary check below).
import { useCallback, useEffect, useState } from "react";
import { useWS } from "../net/useWS";
import { encryptForConnection } from "../net/pwCrypto";
import type { ReqEnvelope, RespEnvelope, User as PbUser } from "../proto/messages";
import { Trans, useT, type TFunction } from "../i18n";
import { asIs, plainNumber, say, unitText, type UiText } from "./uiText";
import "./SettingsForm.css";
import "./UsersPanel.css";
import { refuseOnDemo } from "../net/demo";

type Metrics = { storageMb: number; storagePct: number; activeConnections: number };

// Numbers as toFixed wrote them, in the language's own format. Rounded by
// toFixed first: Intl rounds the shortest decimal form (1.15 -> "1.2")
// where toFixed rounds the exact value ("1.1"), so Intl only lays out
// digits that are already final. "12.5%" is a percent unit: the number
// isn't multiplied by 100 on the way.
const mbText = (t: TFunction, mb: number) => unitText(t, Number(mb.toFixed(0)), "megabyte", 0);
const pctText = (t: TFunction, pct: number) =>
  plainNumber(t, Number(pct.toFixed(1)), { style: "unit", unit: "percent", minimumFractionDigits: 1, maximumFractionDigits: 1, useGrouping: false });

export default function UsersPanel() {
  const t = useT();
  const [isPrimary, setIsPrimary] = useState<boolean | null>(null);
  const [users, setUsers] = useState<PbUser[] | null>(null);
  const [status, setStatus] = useState<{ kind: "error" | "success"; text: UiText } | null>(null);

  const [newUsername, setNewUsername] = useState("");
  const [newPort, setNewPort] = useState("");
  // Issue #103: on by default - a user who can't be reached from outside
  // the house is the unusual case, not the normal one. When set, the
  // device checks the subdomain is actually free on the bridge before
  // creating anything, and refuses rather than handing back an account
  // that looks fine and is silently unreachable.
  const [requestBridgeAccess, setRequestBridgeAccess] = useState(true);
  // A local-only device has no bridge to check the subdomain on, so asking
  // for it would always refuse: no option there, and none requested.
  const [onBridge, setOnBridge] = useState(true);

  // The bridge's own domain, taken from a user that already has a
  // subdomain - the only place the client already knows it, and worth
  // showing so "request bridge access" names the actual address the new
  // user would get. Falls back to generic wording on a device with no
  // bridge-backed users yet.
  const bridgeDomain = users?.find(u => u.bridgeAccess && u.subdomain)
    ?.subdomain.split(".").slice(1).join(".") ?? "";

  const [creating, setCreating] = useState(false);

  // The dialog Add User opens: the new account's first password, set on
  // the device as the account is created, so nobody can sign in first and
  // claim it. Its person changes it later in Change Password.
  const [passwordFor, setPasswordFor] = useState<string | null>(null);
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [passwordError, setPasswordError] = useState<UiText | null>(null);
  // The same 8 the device asks for (session.MinPasswordLen).
  const pwTooShort = newPassword.length > 0 && newPassword.length < 8;
  const pwMismatch = confirmPassword.length > 0 && newPassword !== confirmPassword;
  const canCreate = newPassword.length >= 8 && newPassword === confirmPassword;

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

  useEffect(() => {
    if (!isPrimary) return;
    void (async () => {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqGetBridgeAccess", reqGetBridgeAccess: {} };
      });
      if (resp.payload?.$case === "respBridgeAccess" && !resp.payload.respBridgeAccess.enabled) {
        setOnBridge(false);
      }
    })().catch(() => {}); // unanswered: keep the option, as before
  }, [isPrimary]);

  const fetchMetrics = async (uuid: string) => {
    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqGetUserMetrics", reqGetUserMetrics: { uuid } };
    });
    if (resp.payload?.$case === "respUserMetrics") {
      const m = resp.payload.respUserMetrics;
      setMetrics(prev => ({ ...prev, [uuid]: { storageMb: m.storageMb, storagePct: m.storagePct, activeConnections: m.activeConnections } }));
    }
  };

  const askPassword = () => {
    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
    setStatus(null);
    setNewPassword("");
    setConfirmPassword("");
    setPasswordError(null);
    setPasswordFor(newUsername.trim());
  };

  const closePassword = () => {
    if (creating) return;
    setPasswordFor(null);
    setNewPassword("");
    setConfirmPassword("");
  };

  const createUser = async () => {
    if (passwordFor == null || !canCreate || creating) return;
    setCreating(true);
    setPasswordError(null);
    const name = passwordFor;
    try {
      const password = await encryptForConnection(useWS.request, newPassword);
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = {
          $case: "reqCreateUser",
          reqCreateUser: {
            username: name,
            port: newPort ? parseInt(newPort, 10) : 0,
            requestBridgeAccess: onBridge && requestBridgeAccess,
            password,
          },
        };
      });
      if (resp.payload?.$case === "respUsers") {
        setUsers(resp.payload.respUsers.users);
        setNewUsername("");
        setNewPort("");
        setPasswordFor(null);
        setNewPassword("");
        setConfirmPassword("");
        // Unset: a device older than the password, which created the
        // account without one (its first sign-in sets it).
        const key = resp.payload.respUsers.passwordSet === undefined ? "web.settings.users.created_no_password" : "web.settings.users.created";
        setStatus({ kind: "success", text: (tr) => tr(key, { name }) });
      } else {
        // A UiText is a function: React would call a bare one as an updater.
        const text = resp.errorMessage ? asIs(resp.errorMessage) : say("web.settings.users.create_failed");
        setPasswordError(() => text);
      }
    } catch (err: any) {
      const text = asIs(err?.message ?? String(err));
      setPasswordError(() => text);
    } finally {
      setCreating(false);
    }
  };

  const confirmDelete = async () => {

    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
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
        const name = deleteTarget.username;
        setStatus({ kind: "success", text: (tr) => tr("web.settings.users.deleted", { name }) });
        setDeleteTarget(null);
        setDeleteConfirmText("");
        await loadUsers();
      } else {
        setStatus({ kind: "error", text: resp.errorMessage ? asIs(resp.errorMessage) : say("web.settings.users.delete_failed") });
      }
    } catch (err: any) {
      setStatus({ kind: "error", text: asIs(err?.message ?? String(err)) });
    } finally {
      setDeleting(false);
    }
  };

  // Issue #90: reversible enable/disable, distinct from the permanent
  // Delete below - no type-to-confirm needed, since nothing is destroyed.
  const toggleActive = async (u: PbUser) => {
    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
    setBusyActive(u.uuid);
    setStatus(null);
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqSetUserActive", reqSetUserActive: { uuid: u.uuid, active: !u.active } };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        await loadUsers();
      } else {
        setStatus({ kind: "error", text: resp.errorMessage ? asIs(resp.errorMessage) : say("web.settings.users.update_failed") });
      }
    } catch (err: any) {
      setStatus({ kind: "error", text: asIs(err?.message ?? String(err)) });
    } finally {
      setBusyActive(null);
    }
  };

  if (!isPrimary) return null;

  return (
    <section className="sf-section">
      <h3>{t("web.settings.users.title")}</h3>
      <p className="sf-hint">{t("web.settings.users.hint")}</p>

      {status && <div className={`sf-status ${status.kind}`}>{status.text(t)}</div>}

      {users == null ? (
        <div className="sf-hint">{t("common.loading")}</div>
      ) : (
        <ul className="up-list">
          {users.map(u => {
            const m = metrics[u.uuid];
            return (
              <li key={u.uuid} className="up-item">
                <div className="up-item-main">
                  <div className="up-item-name">
                    {u.username}
                    {!u.active && <span className="up-badge up-badge-muted">{t("web.settings.users.inactive")}</span>}
                  </div>
                  <div className="up-item-sub">
                    {u.bridgeAccess
                      ? t("web.settings.users.port_address", { port: String(u.port), address: u.subdomain })
                      : t("web.settings.users.port_local", { port: String(u.port) })}
                  </div>
                </div>
                <div className="up-item-metrics">
                  {m ? (
                    <span className="sf-hint" style={{ margin: 0 }}>
                      {t("web.settings.users.usage", {
                        size: mbText(t, m.storageMb),
                        percent: pctText(t, m.storagePct),
                        count: m.activeConnections,
                      })}
                    </span>
                  ) : (
                    <button className="sf-btn small" onClick={() => void fetchMetrics(u.uuid)}>{t("web.settings.users.load_usage")}</button>
                  )}
                </div>
                <div className="up-item-actions">
                  <button
                    className="sf-btn small sf-btn-secondary"
                    disabled={busyActive === u.uuid}
                    onClick={() => void toggleActive(u)}
                  >
                    {busyActive === u.uuid ? "…" : u.active ? t("web.settings.users.disable") : t("web.settings.users.enable")}
                  </button>
                  <button className="sf-btn small sf-danger" onClick={() => { if (refuseOnDemo()) return; setDeleteTarget(u); setDeleteConfirmText(""); }}>
                    {t("common.delete")}
                  </button>
                </div>
              </li>
            );
          })}
          {users.length === 0 && <li className="sf-hint">{t("web.settings.users.none")}</li>}
        </ul>
      )}

      <div className="up-add-row">
        <input
          className="sf-input"
          placeholder={t("web.settings.users.username")}
          value={newUsername}
          onChange={(e) => setNewUsername(e.target.value)}
          autoCapitalize="none"
          autoCorrect="off"
        />
        <input
          className="sf-input up-port-input"
          placeholder={t("web.settings.users.port")}
          inputMode="numeric"
          value={newPort}
          onChange={(e) => setNewPort(e.target.value.replace(/\D/g, ""))}
        />
        <button
          className="sf-btn"
          disabled={creating || !/^[a-z0-9-]+$/.test(newUsername.trim())}
          onClick={askPassword}
        >
          {t("web.settings.users.add")}
        </button>
      </div>

      {/* Issue #103 */}
      {onBridge && <label className="up-bridge-opt">
        <input
          type="checkbox"
          checked={requestBridgeAccess}
          onChange={(e) => setRequestBridgeAccess(e.target.checked)}
        />
        <span>
          {t("web.settings.users.request_bridge")}
          <span className="sf-hint">
            {newUsername.trim() && bridgeDomain
              ? t("web.settings.users.reachable_at", { address: `${newUsername.trim()}.${bridgeDomain}` })
              : t("web.settings.users.reachable_own")}
            {" "}{t("web.settings.users.local_hint")}
          </span>
        </span>
      </label>}

      {passwordFor != null && (
        <div className="sf-modal" onClick={closePassword}>
          {/* Not a <form>, and autoComplete off: a browser would offer to
              save this password as the owner's own for the device. */}
          <div
            className="sf-modal-inner up-password"
            onClick={e => e.stopPropagation()}
            onKeyDown={(e) => { if (e.key === "Enter") void createUser(); if (e.key === "Escape") closePassword(); }}
          >
            <h4>{t("web.settings.users.password_title", { name: passwordFor })}</h4>
            <input
              className="sf-input"
              type="password"
              autoComplete="off"
              autoFocus
              aria-label={t("web.settings.users.password")}
              placeholder={t("web.settings.users.password")}
              value={newPassword}
              onChange={(e) => setNewPassword(e.target.value)}
            />
            <input
              className="sf-input"
              type="password"
              autoComplete="off"
              aria-label={t("web.settings.users.password_confirm")}
              placeholder={t("web.settings.users.password_confirm")}
              value={confirmPassword}
              onChange={(e) => setConfirmPassword(e.target.value)}
            />
            {pwTooShort && <div className="sf-note error">{t("web.settings.users.password_too_short")}</div>}
            {pwMismatch && <div className="sf-note error">{t("web.settings.users.password_mismatch")}</div>}
            {passwordError && <div className="sf-note error">{passwordError(t)}</div>}
            <p className="sf-hint">{t("web.settings.users.password_hint", { name: passwordFor })}</p>
            <div className="sf-modal-actions">
              <button type="button" className="sf-btn small sf-btn-secondary" disabled={creating} onClick={closePassword}>{t("common.cancel")}</button>
              <button type="button" className="sf-btn small" disabled={!canCreate || creating} onClick={() => void createUser()}>
                {creating ? t("web.settings.users.creating") : t("web.settings.users.create")}
              </button>
            </div>
          </div>
        </div>
      )}

      {deleteTarget && (
        <div className="sf-modal" onClick={() => setDeleteTarget(null)}>
          <div className="sf-modal-inner" onClick={e => e.stopPropagation()}>
            <p>
              <Trans k="web.settings.users.delete_confirm" args={{ name: deleteTarget.username }} parts={{ b: (s) => <strong>{s}</strong> }} />
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
              <button className="sf-btn small" onClick={() => setDeleteTarget(null)}>{t("common.cancel")}</button>
              <button
                className="sf-btn small sf-danger"
                disabled={deleteConfirmText !== deleteTarget.username || deleting}
                onClick={() => void confirmDelete()}
              >
                {deleting ? t("common.deleting") : t("common.delete")}
              </button>
            </div>
          </div>
        </div>
      )}
    </section>
  );
}
