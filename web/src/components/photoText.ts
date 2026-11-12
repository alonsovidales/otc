// SPDX-License-Identifier: AGPL-3.0-or-later

// Text in photos (release 180, Settings.text_in_photos_enabled): the text
// the device read in a photo (FileExifInfo.text, GetFileInfo's answer: a
// line of it a line, top to bottom), as the viewer's Info shows it under
// Text in this photo, with Copy (MediaViewer.tsx). Nothing to show - no
// block - when the device found none, hasn't read the photo yet, Text in
// photos is off, or the device is from before it.

/** What Info shows and Copy copies of the photo's text: as the device read
 *  it, its lines and their spaces kept, without the blank lines before it
 *  and the spaces and blank lines after it (a Windows line end is a line
 *  end); "" when it holds nothing but spaces. */
export function photoText(text: string | undefined): string {
  if (!text) return "";
  return text.replace(/\r\n?/g, "\n").replace(/^\s*\n/, "").trimEnd();
}
