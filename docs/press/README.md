# Press kit and outreach plan

Where to get Off The Cloud covered, how each outlet takes submissions, and ready-to-send pitches.
The outlets' rules and addresses were checked on their own pages on 16 October 2026.

- `fact-sheet.md`: what OTC is, in one page, for any editor.
- `press-faq.md`: answers to what editors and commenters will ask (Immich, microSD wear, the
  relay, price, AI-assisted development).
- `pitches/`: one file per outlet, with its recipient or form, the subject and the text, in the
  outlet's language. Personalise the greeting, check the [CONFIRM] lines, send one at a time.
- `images/`: press screenshots (2880 × 1800, of the public demo's made-up family) and the real
  device photo.

## 1. Before pitching

These are yours to decide or do:
- **Re-cost the hardware.** The Raspberry Pi 5 8 GB went up to about $175 in April 2026, so the
  "about €250 for 1 TB" on the website's FAQ and in the README is out of date. The pitches leave
  prices out until then.
- **Take real photos.** Send the full-resolution original of the device photo, and if you can, a
  photo with the lid off showing the wiring, and one of the lights. Magazines print at 300 dpi.
  The `docs/promo/` scenes are AI-generated: never send them as photos.
- **The CLA.** `CLA.md` lets you relicense contributions, proprietary included. Expect "open
  core?" questions; a short statement of what stays AGPL avoids them.
- **AI disclosure.** selfh.st's form asks whether AI assisted development, Lemmy requires an
  [AIP] tag and a disclosure, and a 190 KB `CLAUDE.md` sits at the repo root. Say yes, plainly;
  `press-faq.md` has a suggested sentence.

I can do these if you want:
- **Refresh the README's opening:** it still says the apps "are still not published". Add the demo
  link, store badges, screenshots and a short comparison with Immich, PhotoPrism and Nextcloud.
- **Fill in GitHub's About:** a description, the website and topics (`self-hosted`, `raspberry-pi`,
  `photos`, `google-photos-alternative`, ...). It's public, so it needs your OK.
- **Document running your own bridge,** and using OTC behind a VPN or reverse proxy. Lemmy's free
  software waiver and awesome-selfhosted's third-party-dependency field depend on it.
- **Add a CHANGELOG** with readable release notes.

## 2. Order and timing

**Wave 1: now.** These have long lead times or long queues.
1. **Raspberry Pi Official Magazine** (`01`): its December or January issue; November is closed.
   Post `02` with #MakerMonday on a Monday.
2. **Linux Magazine** (`06`): publication comes months after acceptance.
3. **Elektor** (`26`): by about 1 December, for the March–April 2027 "Embedded & AI" issue.
4. **AlternativeTo** (`24`) and **European Alternatives** (`25`): their review queues are long.

**Wave 2: launch week**, once the README is refreshed, ideally once the iPhone app is approved.
In this order, a day or two apart, not all at once:
1. **Hackster** (`09`) page, published a week before, and mirrored on Hackaday.io.
2. **selfh.st** (`20`), Monday to Wednesday, for Friday's newsletter.
3. **r/selfhosted**: write it yourself (see section 4).
4. **Hackaday** (`03`) and **Adafruit** (`05`) the same day, linking the Hackster page.
5. **Show HN**: write it yourself, and be there for a few hours to answer.
6. **Tom's Hardware** (`04`), **XDA** (`21`), **It's FOSS** (`22`), **Linuxiac** (`23`) and **CNX
   Software** (`08`), a few days later, pointing at the first coverage.
7. **Europe:** Korben (`10`), heise/c't (`11`), Tweakers (`13`), Genbeta (`14`), Caschys Blog
   (`12`), Le Alternative (`15`), Pplware (`16`), Next (`17`), t3n (`18`), German Make (`27`) and
   The Register (`07`).

**Wave 3: from January 2027.**
- awesome-selfhosted, from **5 January 2027**: it needs a first release more than four months old,
  and v0.1.0 was 4 September 2026. Write it yourself.
- Product Hunt, once the iPhone app is live.
- YouTube creators, once there's coverage to point to: Awesome Open Source, Servers@Home and DB
  Tech first, Jeff Geerling as a long shot. Offer a pre-flashed card.

One polite follow-up after about a week is fine; more is not. Keep a note of who replied.

## 3. Outlets

| Outlet | Language | Channel | Pitch | Fit |
|---|---|---|---|---|
| Raspberry Pi Official Magazine (ex-MagPi) | EN | magazine@raspberrypi.com, "a few sentences" | 01, 02 | ★★★★★ |
| selfh.st, Self-Host Weekly | EN | selfh.st/submit → Project Launch | 20 | ★★★★★ |
| Hackster.io | EN | your own project page | 09 | ★★★★ |
| Adafruit blog | EN | adafruit.com/contact_us, "Cool thing for the blog" | 05 | ★★★★ |
| Linux Magazine | EN | edit@linux-magazine.com, subject "Proposal" | 06 | ★★★ |
| Hackaday | EN | tips@hackaday.com | 03 | ★★★ |
| Tom's Hardware | EN | tips@tomshardware.com | 04 | ★★★ |
| XDA Developers | EN | xda-developers.com/contact, news tip | 21 | ★★★ |
| It's FOSS | EN | itsfoss.com/contact-us | 22 | ★★★ |
| Linuxiac | EN | info@linuxiac.com | 23 | ★★★★ |
| CNX Software | EN | cnx-software.com/contact-us, not from Gmail | 08 | ★★★ |
| The Register | EN | liam.proven@theregister.com | 07 | ★★★ |
| Elektor | EN | editor@elektor.com, by 1 Dec | 26 | ★★ |
| AlternativeTo | EN | account → Suggest new application | 24 | ★★★★ |
| European Alternatives | EN | account, suggest a product | 25 | ★★★ |
| Korben | FR | korben@korben.info | 10 | ★★★★★ |
| Next (next.ink) | FR | actu@next.ink | 17 | ★★★★ |
| heise / c't | DE | heise.de/kontakt or ct@ct.de | 11 | ★★★★★ |
| Caschys Blog | DE | stadt-bremerhaven.de/tipp-senden | 12 | ★★★★★ |
| t3n | DE | redaktion@t3n.de, subject "Redaktionsanfrage" | 18 | ★★★★ |
| Make (German edition) | DE | heise.de/make/kontakt, dab@make-magazin.de | 27 | ★★★ |
| Tweakers | NL | tweakers.net/submit (account), redactie@tweakers.net | 13 | ★★★★★ |
| Genbeta | ES | genbeta.com/contacto, prensa@genbeta.com | 14 | ★★★★★ |
| Le Alternative | IT | info@lealternative.net | 15 | ★★★★★ |
| Pplware | PT | geral@pplware.com | 16 | ★★★★ |

Also worth a later try:
- **Spanish:** Xataka (prensa@xataka.com), Linux Adictos (linux-adictos@googlegroups.com) and
  Hardware Libre (hwlibre@googlegroups.com). The Genbeta pitch works for each.
- **Italian:** IlSoftware.it (redazione@ilsoftware.it).
- **Portuguese:** SAPO Tek (redacao@teknoticias.pt).
- **French:** Framboise314 (contact@framboise314.fr).
- **Linux desktop:** OMG! Ubuntu, for the Linux app only (omgubuntu.co.uk/tip).
- **Podcast:** a Linux Unplugged "Pick" (linuxunplugged.com/contact).

Skip these:
- Linux Format, which has closed.
- Electromaker and Console.dev, which don't fit.
- The Next Web and EU-Startups, which don't take this kind of story.
- Kuketz-Blog, which takes no press releases.

## 4. Write these yourself

These venues forbid or restrict AI-written posts. Hacker News: "Don't post generated text or
AI-edited text". awesome-selfhosted bans LLM-written contributions. Privacy Guides: "Using AI to
generate post content is forbidden". So there are no drafts for them here, only their rules. Use
`fact-sheet.md` for the facts, and write in your own words.

- **Show HN** (news.ycombinator.com/submit):
  - title starting "Show HN:";
  - something people can try with no sign-up, which the demo is;
  - be around to answer;
  - never ask anyone to upvote.
  It's effectively one shot, so post after the fixes above.
- **r/selfhosted:**
  - OTC is older than three months, so a standalone post is allowed (younger projects go in the
    weekly megathread);
  - "production ready and have docs";
  - expect pushback on AI-assisted code, so disclose it.
- **r/raspberry_pi:** a Show-and-Tell post with photos and how it's built. Read the sidebar first.
- **Lemmy !selfhosted@lemmy.world:**
  - the account must be at least 30 days old, so create one now if you haven't;
  - the [AIP] tag, plus a disclosure or a link to an `ai-declaration.md`;
  - no pasted README.
- **awesome-selfhosted**, from 5 January 2027: a PR to awesome-selfhosted-data adding
  `software/off-the-cloud.yml`, by hand, with the "done by a human" box ticked.
- **Privacy Guides:** a post in the forum's Project Showcase, the only place developers may promote
  their own projects. Being listed takes a threat model and a security white paper.
- **LinuxFr.org** (French, CC BY-SA): a dépêche must be a real write-up, not a press release;
  propose it at linuxfr.org/proposer-un-contenu.
- **noted.lol** contributor article (350–500 words, noted.lol/contribute) and **GNU/Linux.ch**
  (German, gnulinux.ch/mitschreiben): articles by the developer.

## 5. Images

- `images/device.jpg`: the real device. Send the full-resolution original when an outlet asks.
- `images/01-images.jpg` … `09-viewer.jpg`: the web app of the public demo, 2880 × 1800. Its
  photos are AI-generated pictures of a made-up family, so they're free to publish; caption them
  "Off The Cloud demo, with AI-generated sample photos".
- The App Store and Google Play screenshots are in `docs/app-store/` and `docs/play-store/`.
