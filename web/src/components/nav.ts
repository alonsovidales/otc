// SPDX-License-Identifier: AGPL-3.0-or-later

// The app's sections. "Profile" is only an anonymous visitor's landing page
// (the read-only ProfileCard); the editable profile lives in Settings
// (issue #84). "SignIn" is the sign-in form. "PhotoGallery" is Images, the
// whole library or a search, person or group in it (photoFilter.ts);
// "People" and "Collections" are the pages that open it on one of those (a
// collection is an image group in the protocol).
export type TabKey = "Profile" | "Social" | "SignIn" | "AdminPannel" | "PhotoGallery" | "People" | "Collections" | "Settings" | "Notifications" | "Friends";
