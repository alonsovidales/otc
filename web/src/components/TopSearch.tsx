// SPDX-License-Identifier: AGPL-3.0-or-later

// STUB - replaced by the real search box.
type Props = {
  // A filter was picked from anywhere: show Images.
  onShowPhotos: () => void;
  // "All people" in the suggestions: the People page.
  onShowPeople: () => void;
};

export default function TopSearch(_: Props) {
  return <div className="ts-root" />;
}
