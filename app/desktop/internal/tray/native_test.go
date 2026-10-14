// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/i18n"
)

// Windows' dialogs and menus draw the isolation marks around user text as
// boxes: there every one of LRI, RLI, FSI and PDI is left out; elsewhere
// (GTK isolates with them) the text is as it is.
func TestWinText(t *testing.T) {
	// The runtime's own: a folder's name in the Images dialog's title.
	title := i18n.DeskMainImagesKeepTitle("Photos").Render("")
	if !strings.Contains(title, "\u2068Photos\u2069") {
		t.Fatalf("expected the name isolated in %q", title)
	}
	if got := winText("windows", title); got != "Keep “Photos” out of Images?" {
		t.Errorf("windows: %q", got)
	}
	for _, goos := range []string{"linux", "darwin", "freebsd"} {
		if got := winText(goos, title); got != title {
			t.Errorf("%s: %q, want it as it is", goos, got)
		}
	}
	// The device's own words carry them too, and any of the four: LRI,
	// RLI, FSI, PDI. Other text - the older embedding controls, quotes,
	// emoji - stays.
	device := "unlock \u2068/Photos\u2069 first, then \u2066Trip\u2069 \u2067\u05d0\u2069 \u202ax\u202c 📁 “a”"
	if got, want := winText("windows", device), "unlock /Photos first, then Trip \u05d0 \u202ax\u202c 📁 “a”"; got != want {
		t.Errorf("windows:\n got %q\nwant %q", got, want)
	}
	if got := winText("windows", "plain"); got != "plain" {
		t.Errorf("plain: %q", got)
	}
}

// A list's rows are shown without the marks, and the row picked comes back
// as the item it was made from, so a caller finds it in its own list.
func TestListRowsMapBack(t *testing.T) {
	items := []string{"📁 \u2068Fotos\u2069", "‹ Up", "📁 \u2068Docs\u2069"}
	rows := make([]string, len(items))
	for i, s := range items {
		rows[i] = winText("windows", s)
	}
	if rows[0] != "📁 Fotos" || rows[2] != "📁 Docs" {
		t.Fatalf("rows: %q", rows)
	}
	for i, r := range rows {
		if got := fromRow(items, rows, r); got != items[i] {
			t.Errorf("row %d: %q, want %q", i, got, items[i])
		}
	}
	if got := fromRow(items, rows, "typed"); got != "typed" {
		t.Errorf("not a row: %q", got)
	}
}

// Every text for a dialog or the menu goes through nativeText: the package
// calls native.go's wrappers, never zenity's or systray's own calls that
// take text, and the Windows picker turns strings into UTF-16 only through
// nativeText.
func TestNativeTextOnly(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == "native.go" {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, v := range directTextCalls(fset, f) {
			t.Error(v)
		}
	}
	if checked < 5 {
		t.Fatalf("only %d files checked", checked)
	}

	// And the check finds each kind of direct call.
	src := `package tray
func f() {
	_ = zenity.Error("x", zenity.Title("t"))
	mi := systray.AddMenuItem("a", "")
	mi.AddSubMenuItem("b", "")
	mi.SetTooltip("c")
	_, _ = syscall.UTF16PtrFromString("d")
	_ = dlgError("x", dlgTitle("t"), zenity.Width(1))
	_, _ = syscall.UTF16PtrFromString(nativeText("e"))
}`
	f, err := parser.ParseFile(fset, "planted.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := directTextCalls(fset, f); len(got) != 6 {
		t.Errorf("planted: %d found, want 6:\n%s", len(got), strings.Join(got, "\n"))
	}
}

// directTextCalls lists f's calls that hand text to a dialog, the menu or
// Win32 without nativeText.
func directTextCalls(fset *token.FileSet, f *ast.File) []string {
	zenityText := map[string]bool{
		"Info": true, "Error": true, "Warning": true, "Question": true, "Entry": true, "List": true,
		"ListItems": true, "ListMultiple": true, "ListMultipleItems": true, "Notify": true, "Progress": true,
		"Title": true, "OKLabel": true, "CancelLabel": true, "ExtraButton": true, "EntryText": true,
	}
	systrayText := map[string]bool{"AddMenuItem": true, "AddMenuItemCheckbox": true, "SetTitle": true, "SetTooltip": true}
	itemText := map[string]bool{"AddSubMenuItem": true, "AddSubMenuItemCheckbox": true, "SetTitle": true, "SetTooltip": true}
	toUTF16 := map[string]bool{"UTF16PtrFromString": true, "UTF16FromString": true, "StringToUTF16Ptr": true, "StringToUTF16": true}

	var found []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg := ""
		if id, ok := sel.X.(*ast.Ident); ok {
			pkg = id.Name
		}
		at := fset.Position(call.Pos())
		switch {
		case pkg == "zenity" && zenityText[sel.Sel.Name]:
			found = append(found, fmt.Sprintf("%s: zenity.%s - use native.go's wrapper (dlg...), or add one", at, sel.Sel.Name))
		case pkg == "systray" && systrayText[sel.Sel.Name]:
			found = append(found, fmt.Sprintf("%s: systray.%s - use native.go's wrapper, or add one", at, sel.Sel.Name))
		case pkg != "systray" && itemText[sel.Sel.Name]:
			found = append(found, fmt.Sprintf("%s: .%s on a menu item - use native.go's wrapper", at, sel.Sel.Name))
		case (pkg == "syscall" || pkg == "windows") && toUTF16[sel.Sel.Name] && (len(call.Args) == 0 || !isCallTo(call.Args[0], "nativeText")):
			found = append(found, fmt.Sprintf("%s: %s.%s without nativeText - use utf16", at, pkg, sel.Sel.Name))
		}

		return true
	})

	return found
}

func isCallTo(e ast.Expr, fn string) bool {
	c, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := c.Fun.(*ast.Ident)

	return ok && id.Name == fn
}
