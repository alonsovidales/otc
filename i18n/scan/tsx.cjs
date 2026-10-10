// SPDX-License-Identifier: AGPL-3.0-or-later

// The web surface's extractor, run by i18nscan (scan/web.go) with node:
// it parses each file with the web app's own TypeScript compiler and prints
// every literal that could be text, with where it stands. The Go side decides
// what counts (the text tests are shared with the other surfaces).
//
// stdin:  {"typescript": "<web/node_modules/typescript>", "files": [{"rel", "abs"}]}
// stdout: [{"file", "line", "end", "kind", "name", "recv", "fallback", "text"}]
//
// kinds: jsx-text (a run of JSX text, inline elements included), attr (a JSX
// attribute's value; name = the attribute), jsx-expr (a literal shown as a
// JSX child), call (an argument; name = the callee, recv = its receiver),
// prop (an object property's value; name = the key), assign (name = the
// variable or property written), other.
"use strict";

const fs = require("fs");

const input = JSON.parse(fs.readFileSync(0, "utf8"));
const ts = require(input.typescript);

// Inline elements don't split a sentence: <p>Read the <a>policy</a>.</p> is one run.
// Elements on lines of their own are separate pieces of text: JSX drops a
// whitespace gap that holds a newline.
const INLINE = new Set(["a", "b", "strong", "em", "i", "code", "span", "small", "br", "kbd", "sup", "sub", "u", "s",
  "mark", "abbr", "time", "wbr"]);
// Commands and identifiers: a placeholder in the sentence, never text.
const CODE = new Set(["code", "kbd", "samp", "pre", "var"]);

const out = [];

function lineOf(sf, pos) {
  return sf.getLineAndCharacterOfPosition(pos).line + 1;
}

function tagName(el) {
  const opening = ts.isJsxElement(el) ? el.openingElement : el;
  return opening.tagName.getText();
}

function literalText(node) {
  if (ts.isTemplateExpression(node)) {
    return node.head.text + node.templateSpans.map((s) => "{}" + s.literal.text).join("");
  }
  return node.text;
}

function calleeName(expr) {
  if (ts.isIdentifier(expr)) return { name: expr.text, recv: "" };
  if (ts.isPropertyAccessExpression(expr)) {
    const recv = ts.isIdentifier(expr.expression) ? expr.expression.text
      : ts.isPropertyAccessExpression(expr.expression) ? expr.expression.name.text
        : ts.isThis(expr.expression) ? "this" : "";
    return { name: expr.name.text, recv };
  }
  if (ts.isElementAccessExpression(expr)) return calleeName(expr.expression);
  return { name: "", recv: "" };
}

function propName(name) {
  if (!name) return "";
  if (ts.isIdentifier(name) || ts.isStringLiteral(name) || ts.isPrivateIdentifier(name)) return name.text;
  return "";
}

// targetName: what an assignment writes (x, a.b.textContent, this.title).
function targetName(expr) {
  if (ts.isIdentifier(expr)) return expr.text;
  if (ts.isPropertyAccessExpression(expr)) return expr.name.text;
  if (ts.isElementAccessExpression(expr) && ts.isStringLiteral(expr.argumentExpression)) return expr.argumentExpression.text;
  return "";
}

// jsxRuns emits one jsx-text per run of text in an element's children.
function jsxRuns(sf, file, children) {
  let text = "", start = -1, end = -1;
  const flush = () => {
    if (start >= 0 && /\p{L}/u.test(text)) {
      out.push({ file, line: lineOf(sf, start), end: lineOf(sf, end), kind: "jsx-text", text });
    }
    text = ""; start = -1; end = -1;
  };
  const addRun = (nodes) => {
    for (const c of nodes) {
      if (ts.isJsxText(c)) {
        if (c.containsOnlyTriviaWhiteSpaces) {
          if (c.text.includes("\n")) flush(); else text += " ";
          continue;
        }
        const t = c.text.replace(/&(?:[a-zA-Z]+|#\d+|#x[0-9a-fA-F]+);/g, "\u00b7");
        if (/\p{L}/u.test(t)) {
          const lead = c.text.length - c.text.trimStart().length;
          if (start < 0) start = c.getStart(sf) + lead;
          end = c.getEnd() - 1;
        }
        text += t;
      } else if (ts.isJsxExpression(c)) {
        if (!c.expression) continue; // a comment
        if (ts.isStringLiteral(c.expression) || ts.isNoSubstitutionTemplateLiteral(c.expression) ||
          ts.isTemplateExpression(c.expression) || ts.isConditionalExpression(c.expression) ||
          ts.isBinaryExpression(c.expression) || ts.isJsxElement(c.expression) || ts.isJsxFragment(c.expression) ||
          ts.isCallExpression(c.expression) || ts.isParenthesizedExpression(c.expression)) {
          // literals in it are judged on their own (jsx-expr); elements in it are visited
          if (ts.isJsxElement(c.expression) || ts.isJsxFragment(c.expression)) { flush(); }
          else text += " {} ";
        } else {
          text += " {} ";
        }
      } else if ((ts.isJsxElement(c) || ts.isJsxSelfClosingElement(c)) && INLINE.has(tagName(c))) {
        if (ts.isJsxElement(c) && !CODE.has(tagName(c))) addRun(c.children);
        else text += " {} ";
      } else {
        flush();
      }
    }
  };
  addRun(children);
  flush();
}

function classify(lit) {
  let node = lit, fallback = false;
  for (;;) {
    const p = node.parent;
    if (!p) break;
    if (ts.isParenthesizedExpression(p) || ts.isAsExpression(p) || ts.isSatisfiesExpression?.(p) ||
      ts.isTypeAssertionExpression?.(p) || ts.isNonNullExpression(p)) { node = p; continue; }
    if (ts.isConditionalExpression(p)) {
      if (p.condition === node) return null;
      node = p; continue;
    }
    if (ts.isBinaryExpression(p)) {
      const op = p.operatorToken.kind;
      const K = ts.SyntaxKind;
      if (op === K.PlusToken) { node = p; continue; }
      if (op === K.BarBarToken || op === K.QuestionQuestionToken) {
        if (p.right === node) fallback = true;
        node = p; continue;
      }
      if (op === K.AmpersandAmpersandToken) {
        if (p.right !== node) return null;
        node = p; continue;
      }
      if (op === K.EqualsToken || op === K.PlusEqualsToken || op === K.BarBarEqualsToken || op === K.QuestionQuestionEqualsToken) {
        if (p.right !== node) return null;
        return { kind: "assign", name: targetName(p.left), fallback };
      }
      return null; // comparisons, in, instanceof, arithmetic
    }
    if (ts.isTemplateSpan(p)) {
      // a literal inside ${…}: judged with its template's context
      node = p.parent; continue;
    }
    if (ts.isJsxExpression(p)) {
      if (p.parent && ts.isJsxAttribute(p.parent)) return { kind: "attr", name: p.parent.name.getText(), fallback };
      return { kind: "jsx-expr", fallback };
    }
    if (ts.isJsxAttribute(p)) return { kind: "attr", name: p.name.getText(), fallback };
    if (ts.isCallExpression(p) || ts.isNewExpression(p)) {
      if (p.expression === node) return null;
      if (ts.isCallExpression(p) && p.expression.kind === ts.SyntaxKind.ImportKeyword) return null;
      const c = calleeName(p.expression);
      return { kind: "call", name: c.name, recv: c.recv, fallback };
    }
    if (ts.isPropertyAssignment(p)) {
      if (p.name === node) return null;
      return { kind: "prop", name: propName(p.name), fallback };
    }
    if (ts.isVariableDeclaration(p)) return { kind: "assign", name: propName(p.name), fallback };
    if (ts.isPropertyDeclaration(p) || ts.isParameter(p) || ts.isBindingElement(p)) {
      if (p.name === node) return null;
      return { kind: "assign", name: propName(p.name), fallback };
    }
    if (ts.isCaseClause(p) || ts.isElementAccessExpression(p) || ts.isImportDeclaration(p) ||
      ts.isExportDeclaration(p) || ts.isExternalModuleReference(p) || ts.isLiteralTypeNode(p) ||
      ts.isTaggedTemplateExpression(p) || ts.isDecorator(p) || ts.isImportTypeNode?.(p) ||
      ts.isEnumMember(p) && p.name === node || ts.isModuleDeclaration(p) || ts.isExpressionStatement(p) ||
      ts.isPropertySignature(p) || ts.isComputedPropertyName(p) || ts.isSwitchStatement(p)) {
      return null;
    }
    return { kind: "other", fallback };
  }
  return { kind: "other", fallback };
}

for (const f of input.files) {
  const src = fs.readFileSync(f.abs, "utf8");
  const kind = f.rel.endsWith(".tsx") ? ts.ScriptKind.TSX : f.rel.endsWith(".js") ? ts.ScriptKind.JS : ts.ScriptKind.TS;
  const sf = ts.createSourceFile(f.rel, src, ts.ScriptTarget.Latest, true, kind);
  const diags = sf.parseDiagnostics || [];
  if (diags.length > 0) {
    // a file in the middle of an edit: an error, never a lower count
    const d = diags[0];
    const msg = typeof d.messageText === "string" ? d.messageText : d.messageText.messageText;
    out.push({ file: f.rel, line: lineOf(sf, d.start || 0), end: 0, kind: "parse-error", text: msg });
    continue;
  }
  const visit = (node) => {
    if (ts.isJsxElement(node) || ts.isJsxFragment(node)) {
      // an inline element directly inside another element is part of that
      // element's run; anywhere else it starts its own
      const nested = ts.isJsxElement(node) && INLINE.has(tagName(node)) && node.parent &&
        (ts.isJsxElement(node.parent) || ts.isJsxFragment(node.parent));
      if (!nested) jsxRuns(sf, f.rel, node.children);
    }
    if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node) || ts.isTemplateExpression(node)) {
      const c = classify(node);
      if (c) {
        out.push({
          file: f.rel, line: lineOf(sf, node.getStart(sf)), end: lineOf(sf, node.getEnd()),
          kind: c.kind, name: c.name || "", recv: c.recv || "", fallback: !!c.fallback, text: literalText(node),
        });
      }
      if (ts.isTemplateExpression(node)) ts.forEachChild(node, visit);
      return;
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
}

process.stdout.write(JSON.stringify(out));
