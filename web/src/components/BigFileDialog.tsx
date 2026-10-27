// SPDX-License-Identifier: AGPL-3.0-or-later
//
// "This is 1.2 GB; your browser will hold it all in memory before saving.
// Continue?" - asked before a download above bigFile.ts's limit is read
// into memory, with Cancel and Download (Open for a file shown in a tab of
// its own; for several, the biggest one's size). Download is pressed in
// its own click, so a tab opened then isn't taken for a pop-up.
import { useT } from "../i18n";
import { ConfirmDialog } from "./ConfirmDialog";
import { sizeText } from "./sizeText";

export default function BigFileDialog({ bytes, kind, onGo, onCancel }: {
  bytes: number;
  // save: a download; open: shown in a tab of its own; one_of: the
  // biggest of several files.
  kind: "save" | "open" | "one_of";
  onGo: () => void;
  onCancel: () => void;
}) {
  const t = useT();
  const size = sizeText(t, bytes, [0, 1, 1]);
  const go = kind === "open" ? t("common.open") : t("common.download");
  const text = kind === "open"
    ? t("web.shell.big_file.open", { size })
    : kind === "one_of"
      ? t("web.shell.big_file.one_of", { size })
      : t("web.shell.big_file.save", { size });
  return (
    <ConfirmDialog
      title={t("web.shell.big_file.title")}
      text={text}
      confirm={go}
      busyText={go}
      tone="primary"
      onConfirm={async () => {
        onGo();
        return null;
      }}
      onCancel={onCancel}
    />
  );
}
