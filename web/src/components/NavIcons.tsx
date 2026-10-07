// SPDX-License-Identifier: AGPL-3.0-or-later

// The sidebar's icons: 24px outlines, stroke in currentColor, so they take
// the item's colour (dim, or the accent when it is the open section).

type P = { size?: number };

function Svg({ size = 22, children }: { size?: number; children: React.ReactNode }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6"
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {children}
    </svg>
  );
}

export const MenuIcon = ({ size }: P) => <Svg size={size}><path d="M4 7h16M4 12h16M4 17h16" /></Svg>;

export const ImagesIcon = ({ size }: P) => (
  <Svg size={size}><rect x="3.5" y="4.5" width="17" height="15" rx="2.5" /><circle cx="9" cy="10" r="1.6" /><path d="m4 17 4.5-4.5 3.5 3.5 2.5-2.5L20 18" /></Svg>
);

export const GroupsIcon = ({ size }: P) => (
  <Svg size={size}><path d="M5 5.5A2.5 2.5 0 0 1 7.5 3H19v15H7.5A2.5 2.5 0 0 0 5 20.5v-15Z" /><path d="M5 20.5A2.5 2.5 0 0 1 7.5 18H19v3H7.5A2.5 2.5 0 0 1 5 20.5Z" /><path d="M9 7.5h6M9 11h6" /></Svg>
);

export const FilesIcon = ({ size }: P) => (
  <Svg size={size}><path d="M3.5 7.5a2 2 0 0 1 2-2h4l2 2h7a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2v-10Z" /></Svg>
);

export const SocialIcon = ({ size }: P) => (
  <Svg size={size}><path d="M4 5.5h12a1.5 1.5 0 0 1 1.5 1.5v7A1.5 1.5 0 0 1 16 15.5H9l-4 3.5v-3.5H4A1.5 1.5 0 0 1 2.5 14V7A1.5 1.5 0 0 1 4 5.5Z" /><path d="M20.5 9.5v7.5a1.5 1.5 0 0 1-1.5 1.5h-.5V21l-3.5-2.5" /></Svg>
);

export const FriendsIcon = ({ size }: P) => (
  <Svg size={size}><circle cx="9" cy="8" r="3" /><path d="M3.5 19c0-3 2.5-5 5.5-5s5.5 2 5.5 5" /><circle cx="17" cy="9" r="2.5" /><path d="M15.5 14.2c2.4.3 4 2 4 4.8" /></Svg>
);

export const AlertsIcon = ({ size }: P) => (
  <Svg size={size}><path d="M12 3a5 5 0 0 0-5 5v2.7c0 1.15-.45 2.25-1.26 3.06L4.5 15h15l-1.24-1.24A4.33 4.33 0 0 1 17 10.7V8a5 5 0 0 0-5-5Z" /><path d="M9.5 18a2.5 2.5 0 0 0 5 0" /></Svg>
);

export const SettingsIcon = ({ size }: P) => (
  // The gear's outline fills the whole box: drawn at 85% so it weighs
  // the same as the other icons.
  <Svg size={size}><g transform="translate(1.8 1.8) scale(0.85)"><circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09a1.65 1.65 0 0 0-1.08-1.51 1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09a1.65 1.65 0 0 0 1.51-1.08 1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1Z" /></g></Svg>
);

export const StorageIcon = ({ size }: P) => (
  <Svg size={size}><rect x="3.5" y="4.5" width="17" height="6" rx="1.5" /><rect x="3.5" y="13.5" width="17" height="6" rx="1.5" /><path d="M7 7.5h.01M7 16.5h.01" /></Svg>
);
