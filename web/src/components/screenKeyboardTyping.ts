// SPDX-License-Identifier: AGPL-3.0-or-later

// How the on-screen keyboard (ScreenKeyboard.tsx) types: into the focused
// field, as the browser's own editing does, so React's onChange and
// onInput see every key, the field's undo works and a selection is
// replaced. document.execCommand is that editing (deprecated, but the one
// way to it from a page; Chromium, the kiosk's browser, has it); where it
// refuses, the value is set through the element's own setter - the one
// React's value tracking watches - and an input event follows.

import { fieldFrom, type FieldInfo } from "./screenKeyboardLayout";

/** The editable field el is, or null. */
export function fieldOfElement(el: Element | null): FieldInfo | null {
  if (!(el instanceof HTMLElement)) return null;
  const input = el instanceof HTMLInputElement ? el : null;
  const area = el instanceof HTMLTextAreaElement ? el : null;
  return fieldFrom({
    tag: el.tagName,
    type: input ? input.type : "",
    inputMode: el.inputMode || el.getAttribute("inputmode") || "",
    enterHint: el.enterKeyHint || el.getAttribute("enterkeyhint") || "",
    capitalize: el.getAttribute("autocapitalize") || "",
    contentEditable: el.isContentEditable,
    readOnly: !!(input?.readOnly || area?.readOnly),
    disabled: !!(input?.disabled || area?.disabled),
  });
}

/** The field the page's focus is in, if it is one. */
export function focusedField(): { el: HTMLElement; field: FieldInfo } | null {
  const el = document.activeElement;
  const field = fieldOfElement(el);
  return field && el instanceof HTMLElement ? { el, field } : null;
}

/** The text before the cursor (the sentence a capital follows). */
export function textBefore(el: HTMLElement): string {
  if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) {
    let at: number | null = null;
    try {
      at = el.selectionStart;
    } catch {
      // A number or email field has no selection to read.
    }
    return el.value.slice(0, at ?? el.value.length);
  }
  const sel = window.getSelection();
  if (!sel || sel.rangeCount === 0 || !el.contains(sel.anchorNode)) return el.textContent ?? "";
  const range = sel.getRangeAt(0).cloneRange();
  range.selectNodeContents(el);
  range.setEnd(sel.anchorNode!, sel.anchorOffset);
  return range.toString();
}

type TextField = HTMLInputElement | HTMLTextAreaElement;

function setValue(el: TextField, value: string) {
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  const setter = Object.getOwnPropertyDescriptor(proto, "value")?.set;
  if (setter) setter.call(el, value);
  else el.value = value;
}

function selectionOf(el: TextField): [number, number] {
  try {
    const start = el.selectionStart;
    const end = el.selectionEnd;
    if (start !== null && end !== null) return [start, end];
  } catch {
    // No selection on this type.
  }
  return [el.value.length, el.value.length];
}

function placeCursor(el: TextField, at: number) {
  try {
    el.setSelectionRange(at, at);
  } catch {
    // No selection on this type.
  }
}

// The field's text with [start, end) replaced, by hand.
function replaceByHand(el: HTMLElement, text: string, deleting: boolean) {
  if (!(el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement)) return;
  const [from, end] = selectionOf(el);
  let start = from;
  if (deleting && start === end) {
    if (start === 0) return;
    // One character back, a pair of surrogates (an emoji) as one.
    const before = Array.from(el.value.slice(0, start));
    start -= before[before.length - 1].length;
  }
  setValue(el, el.value.slice(0, start) + text + el.value.slice(end));
  placeCursor(el, start + text.length);
  el.dispatchEvent(new InputEvent("input", {
    bubbles: true,
    inputType: deleting ? "deleteContentBackward" : "insertText",
    data: deleting ? null : text,
  }));
}

/** Types text where the cursor is in el. */
export function typeText(el: HTMLElement, text: string): void {
  let done = false;
  try {
    done = document.execCommand("insertText", false, text);
  } catch {
    done = false;
  }
  if (!done) replaceByHand(el, text, false);
}

// A key's press and release on el, as a physical key's: false when a
// handler took the press (preventDefault).
function keyDown(el: HTMLElement, key: string, keyCode: number): boolean {
  const init = { key, code: key, keyCode, which: keyCode, bubbles: true, cancelable: true } as KeyboardEventInit;
  return el.dispatchEvent(new KeyboardEvent("keydown", init));
}
function keyUp(el: HTMLElement, key: string, keyCode: number) {
  const init = { key, code: key, keyCode, which: keyCode, bubbles: true, cancelable: true } as KeyboardEventInit;
  el.dispatchEvent(new KeyboardEvent("keyup", init));
}

/** Backspace in el: its keydown first (a field of chips takes it to drop
 *  the last one), then the character before the cursor or the selection. */
export function deleteBack(el: HTMLElement): void {
  if (keyDown(el, "Backspace", 8)) {
    let done = false;
    try {
      done = document.execCommand("delete", false);
    } catch {
      done = false;
    }
    if (!done) replaceByHand(el, "", true);
  }
  keyUp(el, "Backspace", 8);
}

/** Enter in el: its keydown (the app's own Enter handlers), then what the
 *  browser would do - a new line where one can be written, else the form
 *  sent as by its submit button. */
export function pressEnter(el: HTMLElement, field: FieldInfo): void {
  if (keyDown(el, "Enter", 13)) {
    if (field.multiline) {
      let done = false;
      try {
        done = document.execCommand(el instanceof HTMLTextAreaElement ? "insertText" : "insertLineBreak", false, "\n");
      } catch {
        done = false;
      }
      if (!done) replaceByHand(el, "\n", false);
    } else if (el instanceof HTMLInputElement && el.form) {
      const form = el.form;
      const submitter = form.querySelector<HTMLElement>("button[type=submit], input[type=submit], button:not([type])");
      try {
        if (submitter instanceof HTMLButtonElement || submitter instanceof HTMLInputElement) form.requestSubmit(submitter);
        else form.requestSubmit();
      } catch {
        // A submitter outside the form: sent without one.
        form.requestSubmit();
      }
    }
  }
  keyUp(el, "Enter", 13);
}
