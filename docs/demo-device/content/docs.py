#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The demo device's example files: recipes, travel plans, school letters, house notes, a budget...
# Everything is made up (the family, the school, the shops); no ID, bank or medical details.
#   python3 docs.py OUT
# writes OUT/library/{Recipes,Travel,School,House,Documents}/..., each file dated by its
# modification time, and OUT/work/docs.json (path, date, description) for build.py.
# PDFs: HTML printed by headless Chrome (muted, its own profile, closed when done);
# .docx: textutil; .xlsx: minimal SpreadsheetML written with zipfile; PNG/JPEG: Chrome
# screenshots (the receipt then made to look scanned with ffmpeg); Nora's drawing is SVG crayon
# scribbles rendered by Chrome, then scanned into a PDF.
import datetime as dt, glob, html, json, os, shutil, subprocess, sys, tempfile, zipfile

OUT = sys.argv[1]
LIB = os.path.join(OUT, "library")
TMP = tempfile.mkdtemp(prefix="otc-docs-")
CHROME = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
PROFILE = os.path.join(TMP, "chrome-profile")
TZ = dt.timezone(dt.timedelta(hours=1))
records = []

def when(s):  # "2025-08-22 21:14" local Dutch time (CET/CEST close enough for a file date)
    d = dt.datetime.strptime(s, "%Y-%m-%d %H:%M")
    off = 2 if 3 < d.month < 11 or (d.month == 3 and d.day > 28) or (d.month == 10 and d.day < 26) else 1
    return d.replace(tzinfo=dt.timezone(dt.timedelta(hours=off)))

def done(rel, date, desc):
    p = os.path.join(LIB, rel)
    t = when(date).timestamp()
    os.utime(p, (t, t))
    records.append(dict(path=rel, date=when(date).isoformat(), description=desc))
    print("  ", rel)

def write(rel, text):
    p = os.path.join(LIB, rel)
    os.makedirs(os.path.dirname(p), exist_ok=True)
    open(p, "w", encoding="utf-8").write(text)
    return p

def chrome(args):
    subprocess.run([CHROME, "--headless=new", "--mute-audio", "--disable-gpu", "--no-first-run",
                    "--no-default-browser-check", f"--user-data-dir={PROFILE}", "--hide-scrollbars"] + args,
                   check=True, capture_output=True, timeout=120)

def pdf(rel, body, css="", date="", desc=""):
    src = os.path.join(TMP, "page.html")
    open(src, "w", encoding="utf-8").write(f"<!doctype html><html><head><meta charset='utf-8'><style>{BASE}{css}</style></head><body>{body}</body></html>")
    p = os.path.join(LIB, rel)
    os.makedirs(os.path.dirname(p), exist_ok=True)
    chrome([f"--print-to-pdf={p}", "--no-pdf-header-footer", "file://" + src])
    done(rel, date, desc)

def screenshot(path, body, w, h, css=""):
    src = os.path.join(TMP, "shot.html")
    open(src, "w", encoding="utf-8").write(f"<!doctype html><html><head><meta charset='utf-8'><style>html,body{{margin:0}}{css}</style></head><body>{body}</body></html>")
    chrome([f"--screenshot={path}", f"--window-size={w},{h}", "--force-device-scale-factor=2", "file://" + src])

def docx(rel, body, date, desc):
    src = os.path.join(TMP, "doc.html")
    open(src, "w", encoding="utf-8").write(f"<!doctype html><html><head><meta charset='utf-8'><style>body{{font-family:'Helvetica Neue',Arial;font-size:11pt}}h1{{font-size:18pt}}h2{{font-size:13pt;margin-top:14pt}}</style></head><body>{body}</body></html>")
    p = os.path.join(LIB, rel)
    os.makedirs(os.path.dirname(p), exist_ok=True)
    subprocess.run(["textutil", "-convert", "docx", src, "-output", p], check=True)
    done(rel, date, desc)

# ------------------------------------------------------------------ minimal .xlsx
def col(n):
    s = ""
    while n:
        n, r = divmod(n - 1, 26); s = chr(65 + r) + s
    return s

def xlsx(rel, sheets, date, desc):
    """sheets: [(name, rows, widths)], a row is a list of cells: str, number, or ("=FORMULA", cached value);
    the first row is bold, numbers with a decimal part get two decimals."""
    def cell(ref, v, bold):
        st = ' s="1"' if bold else ""
        if isinstance(v, tuple):
            f, val = v
            return f'<c r="{ref}" s="2"><f>{html.escape(f[1:])}</f><v>{val}</v></c>'
        if isinstance(v, (int, float)):
            return f'<c r="{ref}"{" s=\"2\"" if isinstance(v, float) else st}><v>{v}</v></c>'
        if v is None or v == "":
            return ""
        return f'<c r="{ref}" t="inlineStr"{st}><is><t xml:space="preserve">{html.escape(str(v))}</t></is></c>'
    p = os.path.join(LIB, rel)
    os.makedirs(os.path.dirname(p), exist_ok=True)
    with zipfile.ZipFile(p, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("[Content_Types].xml",
            '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">'
            '<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>'
            '<Default Extension="xml" ContentType="application/xml"/>'
            '<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>'
            '<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>'
            + "".join(f'<Override PartName="/xl/worksheets/sheet{i}.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>' for i in range(1, len(sheets) + 1)) +
            '<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>'
            '</Types>')
        z.writestr("_rels/.rels",
            '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
            '<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>'
            '<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>'
            '</Relationships>')
        stamp = when(date).astimezone(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
        z.writestr("docProps/core.xml",
            '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            '<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" '
            'xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" '
            'xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">'
            f'<dc:title>{html.escape(os.path.basename(rel)[:-5])}</dc:title>'
            f'<dcterms:created xsi:type="dcterms:W3CDTF">{stamp}</dcterms:created>'
            f'<dcterms:modified xsi:type="dcterms:W3CDTF">{stamp}</dcterms:modified></cp:coreProperties>')
        z.writestr("xl/workbook.xml",
            '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            '<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" '
            'xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>'
            + "".join(f'<sheet name="{html.escape(n)}" sheetId="{i}" r:id="rId{i}"/>' for i, (n, _, _) in enumerate(sheets, 1)) +
            '</sheets><calcPr calcId="191029" fullCalcOnLoad="1"/></workbook>')
        z.writestr("xl/_rels/workbook.xml.rels",
            '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
            + "".join(f'<Relationship Id="rId{i}" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet{i}.xml"/>' for i in range(1, len(sheets) + 1)) +
            f'<Relationship Id="rId{len(sheets) + 1}" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>'
            '</Relationships>')
        z.writestr("xl/styles.xml",
            '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            '<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">'
            '<numFmts count="1"><numFmt numFmtId="164" formatCode="#,##0.00"/></numFmts>'
            '<fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font></fonts>'
            '<fills count="3"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill>'
            '<fill><patternFill patternType="solid"><fgColor rgb="FFE8F0E8"/><bgColor indexed="64"/></patternFill></fill></fills>'
            '<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>'
            '<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>'
            '<cellXfs count="3"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>'
            '<xf numFmtId="0" fontId="1" fillId="2" borderId="0" xfId="0" applyFont="1" applyFill="1"/>'
            '<xf numFmtId="164" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/></cellXfs>'
            '<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>')
        for i, (name, rows, widths) in enumerate(sheets, 1):
            cols = "".join(f'<col min="{j}" max="{j}" width="{w}" customWidth="1"/>' for j, w in enumerate(widths, 1))
            # Every row starts with an A cell (an empty string when there is nothing): Quick Look
            # leaves out the rows that don't.
            empty = lambda ref: f'<c r="{ref}" t="inlineStr"><is><t></t></is></c>'
            data = "".join(f'<row r="{r}">' + "".join((cell(f"{col(c)}{r}", v, r == 1) or (empty(f"A{r}") if c == 1 else ""))
                                                      for c, v in enumerate(row, 1)) + "</row>"
                           for r, row in enumerate(rows, 1) if row)
            dim = f"A1:{col(max(len(r) for r in rows))}{len(rows)}"
            z.writestr(f"xl/worksheets/sheet{i}.xml",
                '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
                '<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">'
                f'<dimension ref="{dim}"/><sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews>'
                f'<cols>{cols}</cols><sheetData>{data}</sheetData></worksheet>')
    done(rel, date, desc)

def csv(rel, rows, date, desc):
    import csv as c, io
    b = io.StringIO()
    c.writer(b, lineterminator="\r\n").writerows(rows)
    write(rel, b.getvalue())
    done(rel, date, desc)

BASE = """
@page { size: A4; margin: 18mm 17mm; }
body { font-family: 'Avenir Next', 'Helvetica Neue', Helvetica, Arial, sans-serif; color: #26302b; font-size: 10.5pt; line-height: 1.45; }
h1 { font-family: Georgia, 'Times New Roman', serif; font-weight: normal; font-size: 26pt; margin: 0 0 4pt; color: #2f4a3a; }
h2 { font-size: 12.5pt; text-transform: uppercase; letter-spacing: .08em; color: #8a5a2b; margin: 16pt 0 6pt; }
.sub { color: #6b7570; font-style: italic; margin-bottom: 14pt; }
table { border-collapse: collapse; width: 100%; }
th, td { text-align: left; padding: 5pt 7pt; border-bottom: 1px solid #dfe5df; vertical-align: top; }
th { background: #eef3ee; font-weight: 600; }
.cols { display: flex; gap: 22pt; } .cols > div { flex: 1; }
ul, ol { padding-left: 16pt; margin: 4pt 0; } li { margin: 2pt 0; }
.note { background: #fbf6ec; border-left: 3pt solid #d9a650; padding: 8pt 10pt; margin-top: 14pt; }
.foot { margin-top: 22pt; color: #8a938e; font-size: 9pt; }
"""

def ul(items): return "<ul>" + "".join(f"<li>{i}</li>" for i in items) + "</ul>"
def ol(items): return "<ol>" + "".join(f"<li>{i}</li>" for i in items) + "</ol>"
def table(head, rows): return ("<table><tr>" + "".join(f"<th>{h}</th>" for h in head) + "</tr>" +
                               "".join("<tr>" + "".join(f"<td>{c}</td>" for c in r) + "</tr>" for r in rows) + "</table>")

# ------------------------------------------------------------------ Recipes
RECIPE_CSS = "h1{color:#7a2e1f} h2{color:#7a2e1f}"
pdf("Recipes/Grandma Rosa's lasagna.pdf",
    "<h1>Lasagna della Nonna Rosa</h1><div class='sub'>Grandma Rosa's Christmas lasagna, written down at last - serves 8, about 3 hours (most of it the ragù simmering)</div>"
    "<div class='cols'><div><h2>Ragù</h2>" + ul(["2 tbsp olive oil, 1 knob of butter", "1 onion, 1 carrot, 1 celery stick, very finely chopped",
        "500 g minced beef + 250 g minced pork", "150 ml red wine", "2 tins (800 g) chopped tomatoes + 2 tbsp tomato paste",
        "1 bay leaf, salt, pepper, a pinch of nutmeg", "200 ml whole milk (Rosa's secret: it makes the ragù soft)"]) +
    "<h2>Besciamella</h2>" + ul(["80 g butter", "80 g flour", "1 litre warm milk", "nutmeg, salt"]) +
    "<h2>To assemble</h2>" + ul(["about 12 sheets of fresh egg lasagna", "120 g Parmigiano, grated", "1 ball of mozzarella, torn (optional - Jan likes it)"]) +
    "</div><div><h2>Method</h2>" + ol(["Soften the onion, carrot and celery in the oil and butter on a low heat for 10 minutes. Don't let them brown.",
        "Add both meats and break them up until they lose their pink colour. Pour in the wine and let it bubble away.",
        "Add the tomatoes, paste, bay leaf and seasoning. Simmer with the lid half on for at least 2 hours, stirring now and then. Add the milk in the last half hour.",
        "For the besciamella, melt the butter, stir in the flour for a minute, then whisk in the warm milk a little at a time. Cook until it coats the spoon. Nutmeg and salt.",
        "Oven at 180 °C. In a deep dish: a spoon of ragù, pasta, ragù, besciamella, Parmigiano - repeat 4 or 5 times. Finish with besciamella and plenty of cheese.",
        "Bake 40 minutes until golden and bubbling. <b>Rest it 15 minutes</b> before cutting, or it slides everywhere (Rosa says this every year)."]) +
    "<div class='note'>Make the ragù the day before - it tastes better and Christmas Eve is calmer. For Leo's portion: no wine in a small pan of ragù on the side.</div></div></div>"
    "<div class='foot'>From Rosa's handwritten card, typed up by Lucía after Christmas 2024.</div>",
    RECIPE_CSS, "2024-12-27 21:40", "Grandma Rosa's lasagna recipe (PDF)")
pdf("Recipes/Apple pie (Grandma Rosa).pdf",
    "<h1>Grandma Rosa's apple pie</h1><div class='sub'>With the lattice top Nora learned to weave in November 2024 - one 24 cm pie</div>"
    "<div class='cols'><div><h2>Pastry</h2>" + ul(["300 g plain flour", "200 g cold butter, in cubes", "100 g sugar", "1 egg + 1 yolk", "a pinch of salt, grated zest of 1 lemon"]) +
    "<h2>Filling</h2>" + ul(["1 kg apples (half Elstar, half Goudreinette)", "75 g sugar, 2 tsp cinnamon", "50 g raisins soaked in warm water", "1 tbsp lemon juice, 1 tbsp cornflour"]) +
    "</div><div><h2>Method</h2>" + ol(["Rub the butter into the flour, sugar, salt and zest. Add the egg and yolk and bring together quickly. Wrap and chill 30 minutes.",
        "Peel and slice the apples, mix with the other filling ingredients.",
        "Roll out two thirds of the pastry and line a buttered tin. Fill with the apples.",
        "Roll the rest and cut 10 strips. Weave them over the top: five one way, then fold back every other strip to add the others.",
        "Brush with milk, sprinkle with sugar. 50-55 minutes at 180 °C until deep golden.",
        "Cool on a rack for at least an hour. Serve with whipped cream."]) +
    "</div></div><div class='note'>Nora's tip: use a ruler to cut the strips, and don't worry if the weaving goes wrong - it still tastes the same.</div>",
    RECIPE_CSS, "2024-11-11 20:05", "Grandma Rosa's apple pie recipe (PDF)")
write("Recipes/Sunday pancakes.txt",
"""SUNDAY PANCAKES (pannenkoeken) - makes about 10

250 g flour
2 eggs
500 ml milk
pinch of salt
butter for the pan

1. Flour and salt in a bowl, make a well, crack in the eggs.
2. Whisk in the milk a little at a time until smooth. Rest 15 min if there's time.
3. Hot pan, small knob of butter, a ladle of batter, swirl thin.
4. Flip when the top looks dry (Nora flips, everyone else stands back).

Toppings: stroop, powdered sugar, apple + cinnamon, bacon for Tom.
Leo: just sugar. Always just sugar.
""")
done("Recipes/Sunday pancakes.txt", "2024-02-25 10:12", "Sunday pancakes recipe (text)")
write("Recipes/Pizza dough.md",
"""# Pizza dough (for 4 pizzas)

- 500 g flour (tipo 00, or half 00 and half bread flour)
- 325 ml lukewarm water
- 7 g dried yeast (one sachet)
- 10 g salt
- 1 tbsp olive oil

1. Yeast into the water, wait 5 minutes.
2. Mix with the flour, then add the salt and the oil. Knead 10 minutes until smooth.
3. Cover and leave 2 hours somewhere warm (or overnight in the fridge - even better).
4. Divide into 4 balls, rest 30 minutes, stretch by hand (no rolling pin!).
5. Oven as hot as it goes (250 °C), tray in the oven while it heats. 8-10 minutes.

## Sauce
One tin of good tomatoes, a pinch of salt, a little olive oil, torn basil. Don't cook it.

## Who likes what
- Nora: margherita, extra basil
- Leo: ham and sweetcorn (and he picks off the sweetcorn)
- Tom: nduja and honey
- Lucía: courgette, ricotta, lemon zest
""")
done("Recipes/Pizza dough.md", "2026-09-06 09:30", "Pizza dough recipe (Markdown)")

# ------------------------------------------------------------------ Travel
TRAVEL_CSS = "h1{color:#1f5f7a} h2{color:#c06a2b} th{background:#e6f1f5}"
pdf("Travel/Algarve 2025/Algarve itinerary.pdf",
    "<h1>Algarve, August 2025</h1><div class='sub'>Lucía, Tom, Nora and Leo - with Sanne and Pieter for the first week at the villa in Luz</div>" +
    table(["Day", "Plan", "Notes"], [
        ["Sat 9 Aug", "Morning flight to Lisbon, pick up the hire car. Sunset at Cabo da Roca, night in Sintra.", "Car seats booked for both kids"],
        ["Sun 10 Aug", "Drive south (about 3 h). Lagos old town walk, dinner by the marina.", "Arrive at the villa from 16:00"],
        ["Mon 11 Aug", "Beach morning at Praia do Camilo (go early - the steps!), afternoon at Meia Praia.", "Umbrella, spades, snacks"],
        ["Tue 12 Aug", "Slow day at the villa pool. Market in Lagos in the evening.", ""],
        ["Wed 13 Aug", "Benagil: kayak tour to the cave at 10:00 (Tom and Nora), Lucía and Leo on the beach. Carvoeiro boardwalk in the afternoon.", "Kayak minimum age is 6 - Leo stays on the beach"],
        ["Thu 14 Aug", "Grilled sardines for lunch in Lagos. Ponta da Piedade boat trip if the sea is calm.", ""],
        ["Fri 15 Aug", "Pool day; Sanne and Pieter's last night - barbecue at the villa.", "Shopping list on the fridge"],
        ["Sat 16 Aug", "Porto de Mós beach and the cliff walk towards Ponta da Piedade.", "Sun cream, hats, lots of water"],
        ["Sun 17 Aug", "Sunset picnic on the cliffs at Ponta da Piedade.", ""],
        ["Mon 18 Aug", "Day trip to Sagres and Cabo de São Vicente (lighthouse).", "Windy - bring jumpers"],
        ["Tue 19 Aug", "Last beach day at Meia Praia, pack in the evening.", ""],
        ["Wed 20 Aug", "Drive back to Lisbon airport, afternoon flight home.", "Leave the villa by 10:00"],
    ]) +
    "<div class='note'>Shared costs go in the cost split sheet; everything else we sort out at home.</div>",
    TRAVEL_CSS, "2025-07-20 22:10", "Algarve 2025 day-by-day itinerary (PDF)")
docx("Travel/Algarve 2025/Packing list.docx",
    "<h1>Packing list - Algarve 2025</h1>"
    "<h2>Everyone</h2>" + ul(["Passports / ID cards (check dates!)", "Sun cream (50 for the kids), after-sun", "Hats and sunglasses", "Swimwear x2 each, beach towels", "Reusable water bottles", "Phone chargers + the travel adapter"]) +
    "<h2>Nora</h2>" + ul(["Snorkel and mask", "Two books + the drawing pad", "Sandals and trainers", "Hoodie for the evenings"]) +
    "<h2>Leo</h2>" + ul(["Armbands and the swim shirt", "Bucket and spade (the red one)", "Rabbit (do not forget the rabbit)", "Car games, headphones"]) +
    "<h2>Tom &amp; Lucía</h2>" + ul(["Camera + spare battery + memory card", "Kindle", "Small first-aid kit, plasters, insect spray", "Beach umbrella (check with the villa - they might have one)"]) +
    "<h2>Before we leave</h2>" + ul(["Biscuit to Rosa and Jan on Friday", "Plants: ask the neighbours to water", "Bins out on Thursday", "Turn the heating to holiday mode"]),
    "2025-08-06 21:30", "Algarve packing list (Word)")
SPLIT = [
    ("2025-08-09", "Villa in Luz, week 1 (deposit and balance)", "Tom", 1680.00),
    ("2025-08-10", "Supermarket, first shop", "Pieter", 142.35),
    ("2025-08-10", "Dinner by the marina", "Lucía", 118.60),
    ("2025-08-12", "Lagos market: fruit, cheese, bread", "Sanne", 46.20),
    ("2025-08-13", "Supermarket", "Tom", 98.75),
    ("2025-08-14", "Sardines lunch", "Pieter", 104.00),
    ("2025-08-15", "Barbecue: meat, fish, drinks", "Lucía", 87.40),
    ("2025-08-15", "Villa cleaning fee", "Tom", 90.00),
    ("2025-08-16", "Ice creams and coffees", "Sanne", 23.50),
]
fam = [r for r in SPLIT if r[2] in ("Tom", "Lucía")]
total = round(sum(r[3] for r in SPLIT), 2)
ours = round(sum(r[3] for r in fam), 2)
csv("Travel/Algarve 2025/Cost split with Sanne and Pieter.csv",
    [["Date", "What", "Paid by", "Amount (EUR)", "Split"]] + [[d, w, p, f"{a:.2f}", "50/50"] for d, w, p, a in SPLIT] +
    [[], ["", "Total shared", "", f"{total:.2f}", ""], ["", "Each family's half", "", f"{total / 2:.2f}", ""],
     ["", "Paid by us (Tom + Lucía)", "", f"{ours:.2f}", ""], ["", "Sanne and Pieter owe us", "", f"{ours - total / 2:.2f}", ""]],
    "2025-08-22 21:14", "Holiday cost split with friends (CSV)")
rows = [["Date", "What", "Paid by", "Amount (EUR)", "Split"]] + [[d, w, p, a, "50/50"] for d, w, p, a in SPLIT]
n = len(rows)
rows += [[], ["", "Total shared", "", (f"=SUM(D2:D{n})", total), ""],
         ["", "Each family's half", "", (f"=D{n + 2}/2", round(total / 2, 2)), ""],
         ["", "Paid by us (Tom + Lucía)", "", (f'=SUMIF(C2:C{n},"Tom",D2:D{n})+SUMIF(C2:C{n},"Lucía",D2:D{n})', ours), ""],
         ["", "Sanne and Pieter owe us", "", (f"=D{n + 4}-D{n + 3}", round(ours - total / 2, 2)), ""]]
xlsx("Travel/Algarve 2025/Cost split with Sanne and Pieter.xlsx", [("Algarve 2025", rows, [12, 42, 12, 14, 8])],
     "2025-08-22 21:20", "Holiday cost split with friends (Excel)")
write("Travel/Rome 2024/Rome weekend notes.md",
"""# Rome - long weekend, 19-21 April 2024

## Plan
- **Fri**: land around 14:00, bus into town, drop bags. Walk around Trastevere, gelato, dinner at the trattoria with the checked tablecloths (book for 20:30).
- **Sat**: Colosseum at 9:00 (tickets bought online - on Tom's phone), Roman Forum and Palatine after. Lunch break at the flat. Piazza Navona and the fountains in the afternoon, sunset on Ponte Sisto.
- **Sun**: Villa Borghese gardens - rent one of those bike carts. Pantheon on the way back. Flight at 18:40.

## With the kids
- Leo still naps: the buggy is a must, the flat is close to Trastevere.
- Nora wants to throw a coin in the Trevi fountain.
- Water fountains everywhere (nasoni) - bring the bottles.

## To eat
- Cacio e pepe, supplì, carbonara, pizza al taglio.
- Gelato at least once a day (non-negotiable, says Nora).
""")
done("Travel/Rome 2024/Rome weekend notes.md", "2024-04-12 22:45", "Rome weekend plan (Markdown)")
write("Travel/Dolomites 2024/Hikes and campsite.md",
"""# Dolomites camping - 20-27 July 2024

Campsite near Cortina d'Ampezzo, pitch with electricity. Arrive Saturday evening (long drive - stop in Innsbruck for lunch).

## Hikes (short legs in the team!)
| Hike | Length | Notes |
|---|---|---|
| Lago di Braies lake loop | 3.5 km, flat | Go at sunrise. Rowing boats from 9:30 |
| Tre Cime di Lavaredo loop | 10 km | Toll road to Rifugio Auronzo. Leo in the carrier |
| Seceda (cable car from Ortisei) | 2-4 km | The famous ridge. Nice hut for lunch |
| Alpe di Siusi meadows | as far as we like | Cows! Flowers! Easy for Leo |
| Lago di Sorapis | 12 km | Probably too much this time - next trip |

## Camping kit check
- Tent + footprint, 4 sleeping bags, mats, pillows
- Stove, gas, pan, kettle, coffee (!)
- Head torches, lantern, marshmallows, long sticks
- Warm layers - it gets cold at night up there
""")
done("Travel/Dolomites 2024/Hikes and campsite.md", "2024-07-10 21:05", "Dolomites hiking and camping notes (Markdown)")
write("Travel/Zillertal 2026/Ski week checklist.md",
"""# Ski week - Mayrhofen, 14-21 February 2026

- [x] Apartment booked (ski room, 5 min walk to the Penken gondola)
- [x] Ski school for Leo: Mon-Fri mornings, beginners' group
- [x] Nora: group lessons Mon-Wed, then skiing with us
- [ ] Ski passes: buy online for 6 days, kids under 6 free
- [ ] Rent skis and boots for the kids at the shop next to the gondola
- [ ] Helmets (ours fit, Leo needs a bigger one)
- [ ] Goggles, gloves, neck warmers, thermal layers x2
- [ ] Sun cream and lip balm - the glacier sun is strong
- [ ] Snow chains in the car, check the winter tyres

Day plan ideas: Hintertux glacier on the clearest day, a hut lunch on the Penken, sledging one evening, the swimming pool if it snows all day.
""")
done("Travel/Zillertal 2026/Ski week checklist.md", "2026-02-10 22:20", "Ski week checklist (Markdown)")

# ------------------------------------------------------------------ School
SCHOOL_CSS = "h1{color:#3b4f8f} h2{color:#3b4f8f} th{background:#e9edf8}"
pdf("School/Term calendar 2025-2026.pdf",
    "<h1>Willow Lane Primary School</h1><div class='sub'>School year 2025-2026 - holidays and days without school</div>" +
    table(["", "From", "Until", ""], [
        ["First day of school", "Monday 1 September 2025", "", ""],
        ["Autumn break", "Saturday 18 October 2025", "Sunday 26 October 2025", ""],
        ["Study day (no school)", "Friday 21 November 2025", "", "Teachers' training"],
        ["Christmas holidays", "Saturday 20 December 2025", "Sunday 4 January 2026", "School ends at 12:00 on Friday 19 Dec"],
        ["Spring half-term", "Saturday 14 February 2026", "Sunday 22 February 2026", ""],
        ["Easter", "Friday 3 April 2026", "Monday 6 April 2026", ""],
        ["May holidays", "Saturday 25 April 2026", "Sunday 3 May 2026", "Includes King's Day"],
        ["Ascension", "Thursday 14 May 2026", "Friday 15 May 2026", ""],
        ["Whit Monday", "Monday 25 May 2026", "", ""],
        ["School play (groups 3-5)", "Thursday 18 June 2026", "", "18:30 in the school hall"],
        ["Last day of school", "Friday 3 July 2026", "", "School ends at 12:00"],
        ["Summer holidays", "Saturday 4 July 2026", "Sunday 16 August 2026", ""],
    ]) +
    "<div class='note'>Swimming lessons for group 5 are on Wednesday mornings in the autumn term. Please label all clothes, lunch boxes and water bottles.</div>",
    SCHOOL_CSS, "2025-08-28 20:15", "School term calendar 2025-2026 (PDF)")
docx("School/Letter to Ms Hendriks.docx",
    "<p>Utrecht, 20 May 2025</p><p>Dear Ms Hendriks,</p>"
    "<p>Thank you for the note about the school play on 19 June. Nora is very excited to be the sunflower and has been practising her song at home every evening (Biscuit is a patient audience).</p>"
    "<p>We are happy to make her costume ourselves. Could you let us know which shade of yellow the other flowers are using, so that she matches the group? We can also help with the scenery on the Wednesday afternoons - Tom is handy with a saw and Lucía has plenty of paint left from the garden shed.</p>"
    "<p>Nora's grandparents would love to come too. Would two extra seats be possible, or should they buy tickets at the door?</p>"
    "<p>Kind regards,</p><p>Lucía Moreno and Tom de Vries<br/>(Nora's parents, group 4)</p>",
    "2025-05-20 21:50", "Letter to Nora's teacher about the school play (Word)")
write("School/School play - songs and costume notes.txt",
"""SCHOOL PLAY - "The Garden That Woke Up" - Thu 19 June 2025, 19:00

Nora = Sunflower (song 3 "Follow the Sun", and the last song with everyone)

Costume:
- green dress (the one from Rosa, take in the sides)
- yellow felt petals on a headband: 14 petals, about 10 cm, stitch onto the band
- green tights, brown shoes
- maybe a little face paint: freckles are real, no need :)

Practise: song 3 every evening after dinner. Words on the fridge.
Bring: water bottle, snack for the break, hair clips.
Grandma and Grandpa coming - save two seats, row 3.
""")
done("School/School play - songs and costume notes.txt", "2025-06-02 20:40", "School play notes (text)")

# ------------------------------------------------------------------ House
HOUSE_CSS = "h1{color:#47505a} h2{color:#2a7a6a} th{background:#edf2f1}"
pdf("House/Washing machine - quick guide.pdf",
    "<h1>Washing machine - quick guide</h1><div class='sub'>Our WX-7 front loader in the utility room - the short version of the manual</div>" +
    "<h2>Programmes we use</h2>" + table(["Programme", "Temp.", "Max load", "Time", "For"], [
        ["Cotton", "40 / 60 °C", "8 kg", "2 h 50", "Towels, bedding, the kids' school clothes"],
        ["Eco 40-60", "40-60 °C", "8 kg", "3 h 20", "Normal mixed loads - the cheapest to run"],
        ["Synthetics", "40 °C", "4 kg", "1 h 50", "Sportswear, Tom's shirts"],
        ["Wool / hand wash", "30 °C", "2 kg", "1 h", "Jumpers (lay flat to dry)"],
        ["Quick 30'", "30 °C", "3 kg", "30 min", "Lightly worn clothes only"],
        ["Rinse + spin", "-", "8 kg", "20 min", "After hand washing"],
    ]) +
    "<div class='cols'><div><h2>Detergent drawer</h2>" + ul(["I - pre-wash (we never use it)", "II - main wash: liquid or powder", "Flower - fabric softener, up to the MAX line"]) +
    "<h2>Once a month</h2>" + ul(["Empty and rinse the drain filter (bottom right flap, put a tray and a towel under it first)", "Wipe the door seal, leave the door ajar after washing", "Run an empty 90 °C wash with descaler"]) +
    "</div><div><h2>If it stops</h2>" + table(["Code", "What it means", "What to do"], [
        ["E10", "No water coming in", "Is the tap open? Check the inlet hose isn't kinked"],
        ["E20", "Not draining", "Clean the drain filter, check the hose"],
        ["E40", "Door not closed", "Close firmly until it clicks"],
        ["Shaking", "Load out of balance", "Spread the load, don't wash a single heavy item"],
    ]) + "<h2>Child lock</h2><p>Hold the two buttons with the key symbol for 3 seconds - Leo has found out how to start it.</p></div></div>",
    HOUSE_CSS, "2024-03-03 16:20", "Washing machine quick guide (PDF)")
GARDEN_CSS = HOUSE_CSS + """
.beds { display: grid; grid-template-columns: 1fr 1fr; gap: 10pt; margin-top: 6pt; }
.bed { border: 1.5pt solid #8a6a3a; border-radius: 4pt; padding: 7pt 9pt; background: #f8f3e8; }
.bed b { color: #5a4220; } .bed span { display: inline-block; background: #e3eed9; border-radius: 9pt; padding: 1pt 7pt; margin: 2pt 2pt 0 0; font-size: 9.5pt; }
"""
pdf("House/Garden planting plan 2026.pdf",
    "<h1>Vegetable garden 2026</h1><div class='sub'>Four raised beds behind the shed, 120 x 240 cm each - rotate one place clockwise every year</div>"
    "<div class='beds'>"
    "<div class='bed'><b>Bed 1 - tomatoes &amp; friends</b><br/><span>6 tomatoes (2 cherry)</span><span>basil</span><span>marigolds along the edge</span><span>2 peppers</span></div>"
    "<div class='bed'><b>Bed 2 - roots</b><br/><span>carrots (3 rows)</span><span>beetroot</span><span>spring onions</span><span>radishes for Leo</span></div>"
    "<div class='bed'><b>Bed 3 - beans &amp; peas</b><br/><span>runner beans on the wigwam</span><span>sugar snap peas</span><span>nasturtiums</span></div>"
    "<div class='bed'><b>Bed 4 - leaves &amp; squash</b><br/><span>2 courgettes</span><span>lettuce (sow every 3 weeks)</span><span>spinach</span><span>Nora's strawberries</span></div>"
    "</div><h2>Sowing calendar</h2>" + table(["Crop", "Sow indoors", "Plant out / sow outside", "Harvest"], [
        ["Tomatoes, peppers", "March", "mid May (after the last frost)", "July - October"],
        ["Carrots, beetroot", "-", "April - June", "July - October"],
        ["Radishes", "-", "April - August, every 2 weeks", "4 weeks later"],
        ["Runner beans", "late April", "end of May", "July - September"],
        ["Peas", "-", "March - April", "June - July"],
        ["Courgettes", "late April", "end of May", "July - September"],
        ["Lettuce, spinach", "March", "April - August", "May - October"],
    ]) + "<div class='note'>Seedlings bought at the garden centre on 11 April (receipt in Documents). Water in the evening; compost in October.</div>",
    GARDEN_CSS, "2026-03-28 17:45", "Vegetable garden planting plan (PDF)")
write("House/Wi-Fi for guests.txt",
"""Welcome! Wi-Fi for guests

Network:   DeVries-Guest
Password:  demo-not-a-real-password

(This is the guest network - it can't see our computers or the photo box.)
If it doesn't connect, switch the router off and on again: it's the white box
in the hall cupboard, next to the Off The Cloud box. Please don't unplug that one!
""")
done("House/Wi-Fi for guests.txt", "2024-12-20 18:30", "Guest Wi-Fi instructions (text, fake password)")
write("House/House notes.md",
"""# House notes

## Bins
- Green (garden and food waste): every other Tuesday
- Grey (rest): every other Tuesday, the weeks in between
- Paper: first Friday of the month
- Glass and textile: the containers at the end of the street

## Yearly
- **Boiler service**: every October. Last one: 14 Oct 2024, all fine. Pressure should be 1.5-2 bar.
- **Gutters**: November, after the leaves.
- **Smoke alarms**: test on the first Sunday of every month, batteries in January.
- **Bikes**: tyres and lights in September, before the dark mornings.

## Meter readings (end of year)
| Year | Electricity (kWh) | Gas (m3) | Water (m3) |
|---|---|---|---|
| 2023 | 3,410 | 1,120 | 152 |
| 2024 | 3,180 | 1,045 | 148 |

## Odd jobs list
- [ ] Squeaky stair (third from the top)
- [ ] Re-paint Nora's room - she wants "sunflower yellow"
- [x] New tap washer in the bathroom
- [ ] Shelf for Leo's dinosaurs
""")
done("House/House notes.md", "2025-01-30 21:15", "House notes: bins, yearly jobs, meter readings (Markdown)")

PLAN_SVG = """
<svg xmlns='http://www.w3.org/2000/svg' width='1100' height='760' viewBox='0 0 1100 760'>
 <defs>
  <pattern id='g' width='20' height='20' patternUnits='userSpaceOnUse'><path d='M20 0H0V20' fill='none' stroke='#c9d8e8' stroke-width='1'/></pattern>
  <filter id='rough'><feTurbulence type='fractalNoise' baseFrequency='0.03' numOctaves='2' seed='3'/><feDisplacementMap in='SourceGraphic' scale='4'/></filter>
 </defs>
 <rect width='1100' height='760' fill='#fbfcfe'/><rect width='1100' height='760' fill='url(#g)'/>
 <g font-family='Chalkboard SE, Marker Felt, Comic Sans MS' fill='#26324a'>
  <text x='60' y='70' font-size='34'>Ground floor (sketch)</text>
  <text x='640' y='70' font-size='34'>First floor</text>
 </g>
 <g filter='url(#rough)' fill='none' stroke='#26324a' stroke-width='4' stroke-linecap='round'>
  <rect x='60' y='100' width='500' height='560'/>
  <path d='M60 380H330M330 100V660M330 470H560M200 380V660M430 470V660'/>
  <path d='M120 660v-6M170 660v-6' stroke-width='3'/>
  <rect x='640' y='100' width='400' height='560'/>
  <path d='M640 330H860M860 100V660M640 500H860M860 400H1040M750 500V660'/>
 </g>
 <g filter='url(#rough)' fill='none' stroke='#c0543a' stroke-width='3'>
  <path d='M245 660a45 45 0 0 1 45 -45' /><path d='M330 520a40 40 0 0 1 40 40'/><path d='M860 200a40 40 0 0 0 -40 40'/>
 </g>
 <g font-family='Chalkboard SE, Marker Felt, Comic Sans MS' fill='#26324a' font-size='24'>
  <text x='120' y='240'>Living room</text><text x='120' y='272' font-size='17' fill='#5a6a85'>sofa + Biscuit's bed</text>
  <text x='385' y='280'>Kitchen</text><text x='372' y='310' font-size='17' fill='#5a6a85'>big table</text>
  <text x='92' y='530'>Hall</text><text x='92' y='560' font-size='16' fill='#5a6a85'>OTC box in</text><text x='92' y='580' font-size='16' fill='#5a6a85'>the cupboard</text>
  <text x='226' y='530'>Study</text>
  <text x='350' y='575'>Utility</text><text x='350' y='602' font-size='16' fill='#5a6a85'>washer</text>
  <text x='460' y='575'>WC</text>
  <text x='640' y='705' font-size='18' fill='#5a6a85'>(stairs up to the attic: idea for a 3rd bedroom?)</text>
  <text x='690' y='220'>Our room</text>
  <text x='895' y='250'>Nora</text><text x='895' y='280' font-size='17' fill='#5a6a85'>sunflower yellow!</text>
  <text x='690' y='420'>Bathroom</text>
  <text x='905' y='540'>Leo</text><text x='890' y='570' font-size='17' fill='#5a6a85'>dino shelf</text>
  <text x='660' y='585'>Landing</text>
  <text x='90' y='700' font-size='20'>Garden &#8595; (raised beds behind the shed)</text>
  <text x='420' y='92' font-size='16' fill='#5a6a85'>~ 5 x 9.5 m</text>
 </g>
</svg>"""
p = os.path.join(LIB, "House", "Floor plan sketch.png")
screenshot(p, PLAN_SVG, 1100, 760)
done("House/Floor plan sketch.png", "2024-09-21 22:35", "Hand-drawn floor plan sketch of the house (PNG)")

# ------------------------------------------------------------------ Documents
BUDGET = [("Mortgage", 1150), ("Energy and water", 210), ("Groceries", 780), ("Insurance", 165), ("Childcare and after-school club", 420),
          ("Phones and internet", 85), ("Car, fuel and bikes", 190), ("Kids: clubs, swimming, clothes", 160), ("Eating out and treats", 120),
          ("Gifts and birthdays", 60), ("Holidays fund", 300), ("Savings", 350), ("Off The Cloud subscription", 0.83)]
months = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"]
csv("Documents/Family budget 2026.csv",
    [["Category"] + months + ["Year"]] +
    [[c] + [f"{a:.2f}"] * 12 + [f"{a * 12:.2f}"] for c, a in BUDGET] +
    [["Total"] + [f"{sum(a for _, a in BUDGET):.2f}"] * 12 + [f"{sum(a for _, a in BUDGET) * 12:.2f}"]],
    "2026-01-05 21:30", "Family budget for 2026 (CSV)")
rows = [["Category"] + months + ["Year"]]
for i, (c, a) in enumerate(BUDGET, 2):
    rows.append([c] + [float(a)] * 12 + [(f"=SUM(B{i}:M{i})", round(a * 12, 2))])
last = len(BUDGET) + 1
rows.append(["Total"] + [(f"=SUM({col(j)}2:{col(j)}{last})", round(sum(a for _, a in BUDGET), 2)) for j in range(2, 15)])
rows += [[], ["Income (both salaries, net)"] + [5100.0] * 12 + [(f"=SUM(B{last + 3}:M{last + 3})", 61200.0)],
         ["Left over"] + [(f"={col(j)}{last + 3}-{col(j)}{last + 1}", round(5100 - sum(a for _, a in BUDGET), 2)) for j in range(2, 14)] +
         [(f"=N{last + 3}-N{last + 1}", round((5100 - sum(a for _, a in BUDGET)) * 12, 2))]]
xlsx("Documents/Family budget 2026.xlsx", [("Budget 2026", rows, [32] + [10] * 12 + [12])],
     "2026-01-05 21:45", "Family budget for 2026 (Excel)")
docx("Documents/Family meeting notes - September 2025.docx",
    "<h1>Family meeting - Sunday 7 September 2025</h1><p><i>Everyone at the kitchen table after pancakes. Biscuit asleep under it.</i></p>"
    "<h2>How is school going?</h2>" + ul(["Nora: likes Ms Hendriks, wants to join the school choir (Thursdays after school).", "Leo: first week was 'good but long'. Naps in the afternoon again - that's fine."]) +
    "<h2>Jobs at home</h2>" + ul(["Nora feeds Biscuit in the morning, Leo in the evening (with help).", "Everyone clears their own plate.", "Saturday: tidy bedrooms before screens."]) +
    "<h2>Pocket money</h2>" + ul(["Nora: 2.50 a week, Leo: 1 a week. Paid on Saturdays.", "Nora is saving for a scooter."]) +
    "<h2>Plans</h2>" + ul(["Autumn break: a few days with Rosa and Jan.", "Christmas at Rosa and Jan's again - Lucía and Rosa do the menu.", "Next summer: Leo votes for 'the beach with the cave' again."]) +
    "<h2>Next meeting</h2><p>Sunday 5 October, same time. Nora is the secretary.</p>",
    "2025-09-07 11:40", "Notes from a family meeting (Word)")
docx("Documents/Notes for the babysitter.docx",
    "<h1>Notes for the babysitter</h1>"
    "<h2>Evening routine</h2>" + ol(["18:00 dinner (it's in the fridge, 2 minutes in the microwave).", "18:45 bath for Leo, shower for Nora.", "19:15 Leo in bed: one story (the dinosaur one), night light on, door open a little.", "20:00 Nora reads in bed, lights off at 20:30 (she will ask for 20:45)."]) +
    "<h2>Biscuit</h2>" + ul(["Has had his dinner. A short walk round the block around 19:30 is great, lead on the hook by the door.", "He is not allowed on the sofa (he will try)."]) +
    "<h2>Good to know</h2>" + ul(["Our phone numbers are on the list on the fridge door.", "Wi-Fi: the guest network card is on the fridge too.", "No allergies. Leo doesn't like tomatoes this week.", "Help yourself to anything in the kitchen - there's cake in the tin."]),
    "2025-03-01 17:25", "Notes for the babysitter (Word)")
write("Documents/Gift ideas.md",
"""# Gift ideas

## Nora (9 in December)
- Scooter (she's saving too - we could top up)
- Art set with proper watercolours
- Book: the next one in the dragon series
- Ice skates, one size up

## Leo (5 in March)
- Dinosaur excavation kit
- Balance bike -> real bike with stabilisers?
- Rain boots with frogs (asked for "frog boots" twice)

## Rosa
- Pasta machine (hers is from 1985)
- Theatre tickets for the two of them

## Jan
- New gardening gloves and a kneeler
- Bird feeder for the kitchen window

## Tom
- Waterproof camera bag
- Pizza steel
""")
done("Documents/Gift ideas.md", "2025-11-30 22:05", "Gift ideas for the family (Markdown)")
write("Documents/Shopping list.txt",
"""Shopping list

- milk x2
- bread (wholemeal) + croissants for Sunday
- eggs
- apples (Elstar), bananas, pears
- pasta, tinned tomatoes x3
- parmesan
- yoghurt for the kids' lunches
- coffee beans
- dog food (the big bag) + chews for Biscuit
- bin bags
- birthday card for Sanne
- conkers glue?? (Nora's school project)
""")
done("Documents/Shopping list.txt", "2026-10-10 09:15", "Shopping list (text)")
write("Documents/Leo's 5th birthday - plan.md",
"""# Leo turns 5 - Saturday 14 March 2026

**Theme:** dinosaurs (obviously)

## Guests
- 6 friends from his class + Nora and two of her friends to help
- Grandma Rosa and Grandpa Jan from 15:30

## Plan (14:00 - 17:00)
- 14:00 arrive, dinosaur egg hunt in the garden (painted stones in the flower bed)
- 14:30 presents
- 14:45 balloon games outside if dry, otherwise musical statues
- 15:30 cake! (green dinosaur cake, 5 candles + 1 spare)
- 16:00 dinosaur colouring + fossil digging in the sandpit
- 16:45 goodie bags, pick up

## To buy
- [x] balloons (green, orange), candles
- [x] small dinosaur figures for the goodie bags
- [ ] cake ingredients: 4 eggs, butter, flour, green food colouring
- [ ] paper crowns
- [ ] juice, popcorn, fruit
""")
done("Documents/Leo's 5th birthday - plan.md", "2026-02-28 21:10", "Leo's 5th birthday party plan (Markdown)")
pdf("Documents/Leo's party invitation.pdf",
    "<div class='card'><div class='big'>Roar!</div><h1>Leo is turning 5!</h1>"
    "<p class='lead'>Come to my dinosaur party</p>"
    "<p><b>Saturday 14 March 2026</b><br/>14:00 - 17:00<br/>at our house</p>"
    "<p>Dinosaur costumes welcome (not required).<br/>There will be cake, an egg hunt and fossil digging.</p>"
    "<p class='small'>Please let Lucía know by 7 March if you can come.</p>"
    "<div class='dinos'>&#129430; &#129429; &#129430;</div></div>",
    "@page{margin:0} body{margin:0;background:#e8f4d9} .card{margin:22mm;padding:18mm 16mm;border:6px dashed #5a9a3a;border-radius:24px;background:#fffef6;text-align:center;font-family:'Chalkboard SE','Marker Felt',sans-serif;color:#2b4a1e}"
    ".big{font-size:64pt;color:#e0762b;transform:rotate(-4deg)} h1{font-family:'Chalkboard SE','Marker Felt',sans-serif;color:#2b4a1e;font-size:34pt} .lead{font-size:20pt;color:#e0762b} p{font-size:15pt} .small{font-size:12pt;color:#5a6a50} .dinos{font-size:48pt;margin-top:10mm}",
    "2026-02-28 21:30", "Invitation to Leo's 5th birthday party (PDF)")
pdf("Documents/Christmas 2025 menu.pdf",
    "<h1>Christmas 2025 at Rosa &amp; Jan's</h1><div class='sub'>Who cooks what - 24 and 25 December</div>"
    "<div class='cols'><div><h2>Christmas Eve dinner</h2>" + ul(["Antipasti: salami, olives, grilled peppers, focaccia - <i>Lucía</i>", "Lasagna della Nonna - <i>Rosa</i> (the ragù on the 23rd)", "Green salad with orange and fennel - <i>Tom</i>", "Tiramisu - <i>Rosa and Nora</i>", "Hot chocolate for the kids, coffee and grappa for the grown-ups"]) +
    "</div><div><h2>Christmas Day</h2>" + ul(["Breakfast: kerststol and boiled eggs - <i>Jan</i>", "Lunch: roast chicken with lemon and rosemary, roast potatoes - <i>Tom</i>", "Red cabbage with apple - <i>Jan</i>", "Christmas cookies - <i>Rosa and Leo</i> (baking on the 21st)", "Cheese board in the evening"]) +
    "</div></div><div class='note'>Bring: the big oven dish, extra chairs from our garden, Biscuit's bed and his food. Family photo by the tree after lunch on the 25th - jumpers!</div>",
    "h1{color:#8a1f2b} h2{color:#2f6a3a}", "2025-12-15 20:50", "Christmas 2025 menu and who cooks what (PDF)")

# The receipt: a page from a till roll, photographed flat on a scanner.
RECEIPT = """
<div class='r'><div class='c b big'>TUINCENTRUM KLAPROOSHOF</div><div class='c'>garden centre &middot; Utrecht</div><br/>
<div>11-04-2026 &nbsp; 10:42 &nbsp; till 2 &nbsp; receipt 0418</div><div>----------------------------------------</div>
<div class='l'><span>Tomato plants (6)</span><span>&euro; 11,70</span></div>
<div class='l'><span>Basil, pot</span><span>&euro; 2,49</span></div>
<div class='l'><span>Seed packets x5</span><span>&euro; 9,75</span></div>
<div class='l'><span>Seed potatoes 1 kg</span><span>&euro; 3,99</span></div>
<div class='l'><span>Compost 40 L x2</span><span>&euro; 11,98</span></div>
<div class='l'><span>Strawberry plants (8)</span><span>&euro; 7,92</span></div>
<div class='l'><span>Watering can 5 L</span><span>&euro; 6,49</span></div>
<div class='l'><span>Marigolds tray</span><span>&euro; 4,50</span></div>
<div>----------------------------------------</div>
<div class='l b'><span>TOTAL</span><span>&euro; 58,82</span></div>
<div class='l'><span>Card</span><span>&euro; 58,82</span></div><br/>
<div class='l'><span>incl. VAT 9%</span><span>&euro; 3,42</span></div>
<div class='l'><span>incl. VAT 21%</span><span>&euro; 1,13</span></div><br/>
<div class='c'>Items: 25</div><div class='c'>Thank you and happy gardening!</div></div>"""
raw_png = os.path.join(TMP, "receipt.png")
screenshot(raw_png, RECEIPT, 480, 760,
           "body{background:#fff} .r{width:380px;margin:30px auto;font-family:'Courier New',Courier,monospace;font-size:15px;color:#222;line-height:1.45}"
           ".c{text-align:center}.b{font-weight:bold}.big{font-size:17px}.l{display:flex;justify-content:space-between}")
rel = "Documents/Receipts/Garden centre 2026-04-11.jpg"
p = os.path.join(LIB, rel)
os.makedirs(os.path.dirname(p), exist_ok=True)
subprocess.run(["ffmpeg", "-v", "error", "-y", "-i", raw_png, "-vf",
                "pad=iw+160:ih+120:80:60:color=#d9d6cf,rotate=1.3*PI/180:fillcolor=#d4d1ca:ow=iw:oh=ih,"
                "colorchannelmixer=rr=0.97:gg=0.95:bb=0.90,noise=alls=7:allf=u,gblur=sigma=0.6,eq=contrast=1.05:brightness=-0.02",
                "-q:v", "4", p], check=True)
done(rel, "2026-04-11 14:05", "Scanned garden centre receipt (JPEG)")

# Nora's drawing, scanned to PDF: crayon scribbles drawn as SVG (wobbly strokes, waxy texture).
def scribble(x0, y0, x1, y1, step, color, w=7, slant=0.6, seed=0):
    """A back-and-forth crayon fill over a box (clipped to a shape by the caller)."""
    import random
    r = random.Random(seed)
    pts, x, up = [], x0 - (y1 - y0) * slant, True
    while x < x1 + (y1 - y0) * slant:
        a = (x + r.uniform(-2, 2), (y1 if up else y0) + r.uniform(-4, 4))
        pts.append(f"{a[0] + ((y1 - y0) * slant if up else 0):.0f},{a[1]:.0f}")
        x += step + r.uniform(-2, 3); up = not up
    return f"<polyline points='{' '.join(pts)}' fill='none' stroke='{color}' stroke-width='{w}' stroke-linecap='round' stroke-linejoin='round' opacity='.85'/>"

def figure(cx, top, h, skin, hair, body, kind, seed):
    """A child's drawing of a person: round head, triangle or box body, stick limbs, a smile."""
    hr = h * 0.13
    hy = top + hr
    g = [f"<g filter='url(#wob)'>"]
    if kind in ("mum", "girl"):
        body_pts = f"{cx},{hy + hr} {cx - h * 0.2},{top + h * 0.68} {cx + h * 0.2},{top + h * 0.68}"
    else:
        body_pts = f"{cx - h * 0.12},{hy + hr} {cx + h * 0.12},{hy + hr} {cx + h * 0.13},{top + h * 0.66} {cx - h * 0.13},{top + h * 0.66}"
    cid = f"c{seed}"
    g.append(f"<clipPath id='{cid}'><polygon points='{body_pts}'/></clipPath>")
    g.append(f"<g clip-path='url(#{cid})'>{scribble(cx - h * 0.25, hy + hr, cx + h * 0.25, top + h * 0.7, 11, body, 8, 0.5, seed)}</g>")
    g.append(f"<polygon points='{body_pts}' fill='none' stroke='{body}' stroke-width='6' stroke-linejoin='round'/>")
    leg = top + h * 0.67
    g.append(f"<path d='M{cx - h * 0.06},{leg} l-{h * 0.04},{h * 0.3} m{h * 0.15},-{h * 0.3} l{h * 0.04},{h * 0.3}' stroke='#3a3a3a' stroke-width='6' fill='none' stroke-linecap='round'/>")
    g.append(f"<path d='M{cx - h * 0.1},{hy + hr * 1.7} l-{h * 0.17},{h * 0.12} M{cx + h * 0.1},{hy + hr * 1.7} l{h * 0.17},-{h * 0.08}' stroke='{skin}' stroke-width='7' fill='none' stroke-linecap='round'/>")
    if kind == "mum":
        g.append(f"<path d='M{cx - hr * 1.1},{hy} q-{hr * .5},{hr * 1.6} -{hr * .2},{hr * 2.4} M{cx + hr * 1.1},{hy} q{hr * .5},{hr * 1.6} {hr * .2},{hr * 2.4}' stroke='{hair}' stroke-width='14' fill='none' stroke-linecap='round'/>")
    if kind == "girl":
        g.append(f"<path d='M{cx + hr * .9},{hy + hr * .3} q{hr * .6},{hr * 1.2} {hr * .2},{hr * 2.6}' stroke='{hair}' stroke-width='12' fill='none' stroke-linecap='round' stroke-dasharray='10 4'/>")
    g.append(f"<circle cx='{cx}' cy='{hy}' r='{hr}' fill='{skin}' stroke='#c98a6a' stroke-width='4' opacity='.95'/>")
    if kind == "boy":
        g.append("".join(f"<circle cx='{cx + dx * hr}' cy='{hy - hr * .85 + abs(dx) * hr * .25}' r='{hr * .32}' fill='none' stroke='{hair}' stroke-width='7'/>" for dx in (-0.8, -0.3, 0.2, 0.7)))
    else:
        g.append(f"<path d='M{cx - hr * 1.05},{hy - hr * .1} q{hr * 1.05},-{hr * 1.6} {hr * 2.1},0' stroke='{hair}' stroke-width='13' fill='none' stroke-linecap='round'/>")
    if kind == "dad":
        g.append(f"<path d='M{cx - hr * .8},{hy + hr * .35} q{hr * .8},{hr * 1.2} {hr * 1.6},0' stroke='{hair}' stroke-width='9' fill='none' stroke-linecap='round'/>")
    g.append(f"<circle cx='{cx - hr * .38}' cy='{hy - hr * .15}' r='5' fill='#222'/><circle cx='{cx + hr * .38}' cy='{hy - hr * .15}' r='5' fill='#222'/>")
    if kind == "leo":  # his round glasses
        g.append(f"<circle cx='{cx - hr * .38}' cy='{hy - hr * .15}' r='{hr * .3}' fill='none' stroke='#2b4a8a' stroke-width='5'/>"
                 f"<circle cx='{cx + hr * .38}' cy='{hy - hr * .15}' r='{hr * .3}' fill='none' stroke='#2b4a8a' stroke-width='5'/>"
                 f"<line x1='{cx - hr * .08}' y1='{hy - hr * .15}' x2='{cx + hr * .08}' y2='{hy - hr * .15}' stroke='#2b4a8a' stroke-width='4'/>")
    g.append(f"<path d='M{cx - hr * .45},{hy + hr * .3} q{hr * .45},{hr * .45} {hr * .9},0' stroke='#c0392b' stroke-width='4' fill='none' stroke-linecap='round'/>")
    g.append("</g>")
    return "".join(g)

W, H = 1754, 1240
parts = [f"<svg xmlns='http://www.w3.org/2000/svg' width='{W}' height='{H}' viewBox='0 0 {W} {H}'>",
         "<defs><filter id='wob'><feTurbulence type='fractalNoise' baseFrequency='0.02' numOctaves='2' seed='4'/>"
         "<feDisplacementMap in='SourceGraphic' scale='9'/></filter>"
         "<filter id='wax'><feTurbulence type='fractalNoise' baseFrequency='0.9' numOctaves='2' seed='2' result='n'/>"
         "<feColorMatrix in='n' type='matrix' values='0 0 0 0 1  0 0 0 0 1  0 0 0 0 1  0 0 0 -1.5 1.7' result='holes'/>"
         "<feComposite in='SourceGraphic' in2='holes' operator='in'/></filter></defs>",
         f"<rect width='{W}' height='{H}' fill='#fdfcf8'/>", "<g filter='url(#wax)'>"]
# sky band, sun, clouds
parts.append(f"<g filter='url(#wob)'>{scribble(0, 0, W, 70, 16, '#7fb3e6', 9, 0.4, 1)}</g>")
parts.append("<g filter='url(#wob)'><clipPath id='sun'><circle cx='230' cy='230' r='105'/></clipPath>"
             f"<g clip-path='url(#sun)'>{scribble(120, 120, 340, 340, 12, '#f5c518', 10, 0.5, 2)}</g>"
             "<circle cx='230' cy='230' r='105' fill='none' stroke='#f0a500' stroke-width='8'/>" +
             "".join(f"<line x1='{230 + 130 * __import__('math').cos(a / 10 * 6.283):.0f}' y1='{230 + 130 * __import__('math').sin(a / 10 * 6.283):.0f}' "
                     f"x2='{230 + 190 * __import__('math').cos(a / 10 * 6.283):.0f}' y2='{230 + 190 * __import__('math').sin(a / 10 * 6.283):.0f}' "
                     "stroke='#f0a500' stroke-width='9' stroke-linecap='round'/>" for a in range(10)) + "</g>")
for cx, cy in ((720, 200), (1180, 150)):
    parts.append(f"<g filter='url(#wob)' fill='none' stroke='#5b8fd6' stroke-width='7'>" +
                 "".join(f"<circle cx='{cx + dx}' cy='{cy + dy}' r='{r}'/>" for dx, dy, r in ((-70, 15, 45), (-15, -15, 60), (55, 10, 48), (0, 30, 40))) + "</g>")
# house
parts.append("<g filter='url(#wob)'><clipPath id='hb'><rect x='1250' y='520' width='380' height='420'/></clipPath>"
             f"<g clip-path='url(#hb)'>{scribble(1250, 520, 1630, 940, 15, '#f2b880', 9, 0.5, 3)}</g>"
             "<rect x='1250' y='520' width='380' height='420' fill='none' stroke='#8a5a2b' stroke-width='8'/>"
             "<clipPath id='roof'><polygon points='1220,530 1440,320 1660,530'/></clipPath>"
             f"<g clip-path='url(#roof)'>{scribble(1220, 320, 1660, 530, 13, '#d63b2f', 10, 0.5, 4)}</g>"
             "<polygon points='1220,530 1440,320 1660,530' fill='none' stroke='#a52a20' stroke-width='8' stroke-linejoin='round'/>"
             "<rect x='1395' y='760' width='95' height='180' fill='#8a5a2b' opacity='.8'/><circle cx='1470' cy='855' r='7' fill='#f5c518'/>"
             "<rect x='1290' y='590' width='90' height='90' fill='#bfe3f7' stroke='#3b6ea5' stroke-width='6'/><path d='M1335 590v90M1290 635h90' stroke='#3b6ea5' stroke-width='5'/>"
             "<rect x='1500' y='590' width='90' height='90' fill='#bfe3f7' stroke='#3b6ea5' stroke-width='6'/><path d='M1545 590v90M1500 635h90' stroke='#3b6ea5' stroke-width='5'/>"
             "<rect x='1530' y='360' width='40' height='90' fill='#8a5a2b'/></g>")
# grass and flowers
parts.append(f"<g filter='url(#wob)'>{scribble(0, 1040, W, H, 14, '#5aa832', 10, 0.25, 5)}"
             f"<polyline points='{' '.join(f'{x},{1040 - (25 if (x // 30) % 2 else 0)}' for x in range(0, W + 30, 30))}' fill='none' stroke='#3f8a22' stroke-width='7'/></g>")
for i, (fx, col) in enumerate(((90, '#e84a8a'), (560, '#8e5ad6'), (1090, '#e84a8a'), (1700, '#f39c12'))):
    parts.append(f"<g filter='url(#wob)'><line x1='{fx}' y1='1120' x2='{fx}' y2='1000' stroke='#3f8a22' stroke-width='7'/>" +
                 "".join(f"<circle cx='{fx + dx}' cy='{985 + dy}' r='17' fill='{col}'/>" for dx, dy in ((-20, 0), (20, 0), (0, -20), (0, 20))) +
                 f"<circle cx='{fx}' cy='985' r='13' fill='#f5c518'/></g>")
# the family, left to right: mum, dad, Nora, Leo - and Biscuit
parts.append(figure(330, 470, 560, '#f3c7a8', '#3b2618', '#e85d8a', "mum", 11))
parts.append(figure(560, 440, 590, '#f6d0b5', '#9a6a3a', '#3b7dd8', "dad", 12))
parts.append(figure(780, 590, 430, '#f3c7a8', '#8a5a2b', '#f5c518', "girl", 13))
parts.append(figure(960, 680, 340, '#f1c9a5', '#3b2618', '#e8452c', "leo", 14))
parts.append("<g filter='url(#wob)'><clipPath id='dog'><ellipse cx='1140' cy='930' rx='105' ry='60'/></clipPath>"
             f"<g clip-path='url(#dog)'>{scribble(1030, 860, 1250, 1000, 11, '#e0a33a', 9, 0.4, 6)}</g>"
             "<ellipse cx='1140' cy='930' rx='105' ry='60' fill='none' stroke='#b8791c' stroke-width='7'/>"
             "<circle cx='1240' cy='860' r='52' fill='#e8b04a' stroke='#b8791c' stroke-width='7'/>"
             "<path d='M1205 830 q-25 30 -5 70 M1275 830 q25 30 5 70' stroke='#a8691c' stroke-width='16' fill='none' stroke-linecap='round'/>"
             "<circle cx='1225' cy='850' r='6' fill='#222'/><circle cx='1257' cy='850' r='6' fill='#222'/><ellipse cx='1242' cy='875' rx='11' ry='8' fill='#222'/>"
             "<path d='M1242 885 q0 22 10 25' stroke='#e74c3c' stroke-width='9' fill='none' stroke-linecap='round'/>"
             "<path d='M1040 905 q-50 -40 -35 -90' stroke='#b8791c' stroke-width='12' fill='none' stroke-linecap='round'/>"
             "<path d='M1075 975 v60 M1110 980 v60 M1170 980 v60 M1205 975 v60' stroke='#b8791c' stroke-width='12' stroke-linecap='round'/>"
             "<path d='M1203 893 q37 20 74 0' stroke='#d63b2f' stroke-width='8' fill='none'/></g>")
parts.append("</g></svg>")
drawing_png = os.path.join(TMP, "drawing.png")
screenshot(drawing_png, "".join(parts), W, H, "body{background:#fdfcf8}")
img = os.path.join(TMP, "drawing.jpg")
subprocess.run(["ffmpeg", "-v", "error", "-y", "-i", drawing_png, "-vf",
                "scale=1754:1240,colorchannelmixer=rr=0.99:gg=0.98:bb=0.95,noise=alls=5:allf=u", "-q:v", "3", img], check=True)
pdf("School/Nora's drawing (scan).pdf",
    f"<div class='bed'><img src='file://{img}'/></div>",
    "@page{size:A4 landscape;margin:0} body{margin:0} .bed{width:297mm;height:209mm;background:#ecebe6;display:flex;align-items:center;justify-content:center;overflow:hidden}"
    "img{width:282mm;height:auto;transform:rotate(-0.7deg);box-shadow:0 0 3px rgba(0,0,0,.25)}",
    "2025-10-03 19:20", "Nora's crayon drawing of the family and Biscuit, scanned (PDF)")
shutil.copy(drawing_png, os.path.join(OUT, "work", "nora-drawing.png"))

json.dump(records, open(os.path.join(OUT, "work", "docs.json"), "w"), ensure_ascii=False, indent=1)
shutil.rmtree(TMP, ignore_errors=True)
print(len(records), "files")
