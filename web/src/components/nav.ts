// SPDX-License-Identifier: AGPL-3.0-or-later

// The app's sections. "Profile" is only an anonymous visitor's landing page
// (the read-only ProfileCard); the editable profile lives in Settings
// (issue #84). "SignIn" is the sign-in form. "PhotoGallery" is Images, the
// whole library or a search, person or group in it (photoFilter.ts);
// "People" and "Groups" are the pages that open it on one of those.
export type TabKey = "Profile" | "Social" | "SignIn" | "AdminPannel" | "PhotoGallery" | "People" | "Groups" | "Settings" | "Notifications" | "Friends";
