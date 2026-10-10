// SPDX-License-Identifier: AGPL-3.0-or-later
//
// docs/promo only (not part of the app): stands in for web/src/net/useWS.ts so the
// real web app renders offline with the sample library in ../source/library, for
// screenshots. serve.mjs aliases every import of net/useWS to this file.

// What a signed-in browser would have stored: a live session token (restored
// on load through authWithToken below), the Images tab, push already asked.
try {
  localStorage.setItem("otc_session_token", JSON.stringify({ token: "promo", expiresAtMs: Date.now() + 30 * 60 * 1000 }));
  localStorage.setItem("otc_last_tab", "PhotoGallery");
  localStorage.setItem("otc_push_prompted", "1");
  localStorage.setItem("otc_menu_open", "1");
} catch { /* not needed to render */ }

export class NewDevice extends Error {
  readonly isPrimary: boolean;
  constructor(isPrimary: boolean) {
    super("This device isn't set up yet.");
    this.isPrimary = isPrimary;
  }
}

// The library, newest first: sample photo number and the day it was taken.
const cPhotos: [string, string][] = [
  ["01", "2026-10-09"], ["02", "2026-10-08"], ["03", "2026-10-07"], ["06", "2026-10-05"], ["05", "2026-10-04"], ["04", "2026-10-02"],
  ["09", "2026-09-20"], ["08", "2026-09-14"], ["07", "2026-09-08"], ["19", "2026-09-03"], ["10", "2026-09-02"], ["20", "2026-09-01"],
  ["11", "2026-08-24"], ["12", "2026-08-23"], ["18", "2026-08-22"], ["13", "2026-08-19"], ["17", "2026-08-18"],
  ["16", "2026-08-15"], ["14", "2026-08-09"], ["15", "2026-08-08"],
];
// Older months for the date scrubber (photos never loaded: nobody scrolls).
const cOlder: [string, number][] = [
  ["2026-07", 48], ["2026-06", 31], ["2026-04", 22], ["2026-01", 17], ["2025-12", 36], ["2025-08", 124], ["2025-07", 58],
  ["2025-04", 19], ["2024-12", 41], ["2024-08", 87], ["2024-05", 26], ["2023-08", 64], ["2023-03", 15], ["2022-07", 52],
  ["2021-09", 33], ["2020-08", 47], ["2019-07", 12], ["2018-05", 9],
];

const params = new URLSearchParams(location.search);
const order = (params.get("photos") ?? "").split(",").filter(Boolean);
const photos = order.length ? order.map((n, i) => [n, cPhotos.find((p) => p[0] === n)?.[1] ?? cPhotos[i]?.[1] ?? "2026-08-01"] as [string, string]) : cPhotos;

const lib = (n: string) => `/promo/source/library/${n}.jpg`;
const bytesOf = async (n: string) => new Uint8Array(await (await fetch(lib(n))).arrayBuffer());

let filesP: Promise<unknown[]> | null = null;
const files = () => (filesP ??= Promise.all(photos.map(async ([n, day]) => {
  const content = await bytesOf(n);
  const d = new Date(`${day}T${10 + (Number(n) % 8)}:${String(Number(n) * 7 % 60).padStart(2, "0")}:00`);
  return {
    hash: `promo${n}`, mime: "image/jpeg", created: d, modified: d, path: `/Photos/IMG_${4000 + Number(n)}.jpg`,
    size: content.length, content, uploadOnly: false, versions: 1, size64: BigInt(content.length), outOfImages: false, thumbnailSmall: true,
  };
})));

const buckets = () => {
  const m = new Map<string, number>();
  for (const [, day] of photos) m.set(day.slice(0, 7), (m.get(day.slice(0, 7)) ?? 0) + 1);
  for (const [month, count] of cOlder) m.set(month, count);
  return [...m.entries()].sort((a, b) => b[0].localeCompare(a[0])).map(([month, count]) => ({ month, count }));
};

const ok = (payload: unknown) => ({ id: 0, error: false, errorMessage: "", errorCode: "", payload });

async function request(fill: (e: any) => void): Promise<any> {
  const e: any = {};
  fill(e);
  const c: string = e.payload?.$case ?? "";
  const req = e.payload?.[c] ?? {};
  switch (c) {
    case "reqGetPubKey":
      return ok({ $case: "respPubKey", respPubKey: { publicKey: new Uint8Array(), isNewDevice: false, isPrimary: true } });
    case "reqGetStatus":
      return ok({ $case: "respStatus", respStatus: {
        online: true, disks: 2, errors: [], raidSize: 474000, raidUsage: 128600, diskSize: 29000, diskUsage: 9100,
        cpuUsagePrc: 6, memSize: 8000, memUsage: 2300, raidState: 2, raidLevel: "raid1", raidDevicesActive: 2, raidSyncPercent: 0,
      } });
    case "reqSearchPhotos": {
      const filtered = (req.tags?.length || req.personIds?.length || req.groupId) || req.token;
      return ok({ $case: "respListOfFiles", respListOfFiles: { files: filtered ? [] : await files(), token: "", outOfImagesSupported: true, folderOutOfImages: false, askAgainFrom: 0 } });
    }
    case "reqPhotoDateBuckets":
      return ok({ $case: "respPhotoDateBuckets", respPhotoDateBuckets: { buckets: buckets() } });
    case "reqGetProfile":
      return ok({ $case: "respProfile", respProfile: { name: "Home", image: await bytesOf("avatar"), text: "", domain: "pit.off-the.cloud" } });
    case "reqGetSettings":
      return ok({ $case: "respSettings", respSettings: { domain: "pit.off-the.cloud", bridgeSecret: "", faceRecognitionEnabled: true, socialStorageLimitMb: 0, socialStorageUsedBytes: 0n, imageTaggingEnabled: true } });
    case "reqGetNotificationCount":
      return ok({ $case: "respNotificationCount", respNotificationCount: { unacknowledgedCount: 0 } });
    case "reqListPeople":
      return ok({ $case: "respPeople", respPeople: { people: [] } });
    case "reqGetTags":
      return ok({ $case: "respTagsList", respTagsList: { tags: [] } });
    case "reqListImageGroups":
      return ok({ $case: "respImageGroups", respImageGroups: { groups: [] } });
    default:
      console.log("[promo] unanswered", c);
      return ok({ $case: "respAck", respAck: { ok: true, errorMsg: "", code: "", retryAfterSeconds: 0 } });
  }
}

let setAuth: ((v: boolean) => void) | null = null;

function UseWS() {
  const connected = () => true;
  const init = (_endpoint: string, onAuth: (v: boolean) => void) => { setAuth = onAuth; };
  const authWithToken = async (_token: string) => { setTimeout(() => setAuth?.(true), 0); return true; };
  const sendAuth = async () => { setTimeout(() => setAuth?.(true), 0); return true; };
  const refreshSessionToken = async () => {};
  const passwordChanged = () => {};
  const ws = { connected: true, onMessage: () => {}, request: () => Promise.reject(new Error("promo")) };
  return { connected, request, sendAuth, authWithToken, refreshSessionToken, passwordChanged, init, ws };
}

export const useWS = UseWS();
