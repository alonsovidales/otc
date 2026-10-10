// Fixture: the web surface.
import { useState } from "react";
import "./Panel.css";

const EXPLAIN = "Photos here aren't tagged or shown in Images.";
const KEY = "otc_menu_open";
const CLASSES = "flex items-center gap-2";

export default function Panel({ busy }: { busy: boolean }) {
  const [error, setError] = useState("");
  const [tab, setTab] = useState("photos");
  const save = async () => {
    try {
      localStorage.setItem(KEY, "1");
      console.log("Saving the panel now");
    } catch (e) {
      setError("Could not save the panel.");
      setError((e as Error).message || "Something went wrong");
    }
    if (tab === "Photos and videos") return;
  };
  return (
    <div className="panel" aria-label="Settings panel" data-testid="panel-root">
      <h2>Your <b>photos</b> and videos</h2>
      <p title="More about this">{EXPLAIN}</p>
      <button onClick={save}>{busy ? "Saving…" : "Save"}</button>
      <span>{error}</span>
      <span>&nbsp;·&nbsp;</span>
      <span>{"Off The Cloud"}</span>
      <code>DEVICE_UUID=abc</code>
      <a href="/privacy">privacy</a>
      <input placeholder="Your name" type="text" />
      <span>Delete everything</span> {/* i18n-ignore: fixture for the escape hatch */}
      <span className={CLASSES}>{tab === "photos" ? "Photos" : "Files"}</span>
    </div>
  );
}
