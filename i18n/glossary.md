# Glossary and style guide

This is for whoever translates Off The Cloud, a person or Claude. `docs/i18n.md` describes how
localization works; this file says which words to use and how to write them. The term lists are
`i18n/glossary/<code>.json`, and the names that are never translated are
`i18n/glossary/never.json`. Those files are the source of truth: the tables at the end of this
file are copies of them.

The languages are Spanish (es), French (fr), German (de), Italian (it), European Portuguese (pt)
and Dutch (nl). English is the source.

## How the check uses these files

`make i18n-check` (i18ngen) reads them for every translated text, form by form:

- **Never translated** (`never.json`): when the English contains one of these names, written
  exactly so (same case), the translation must contain it written exactly so. A miss is an error.
- **Enforced terms** (`"enforce": true`): when the English contains `en` as a whole word,
  ignoring case, the translation must contain `text`, ignoring case. A miss is a warning, and an
  error with `-strict`.
- **Forbidden words**: a translation that contains one as a whole word, ignoring case, is
  reported the same way.
- Terms with `"enforce": false` are not checked. They are still the words to use; `note` says
  why and how.

A term is enforced only when every correct translation of every English text that contains it
will contain the translated words, spelled exactly so. That is why verbs and words that decline
are not enforced, and why singular and plural are separate entries when the plural is not the
singular plus a few letters (colección, colecciones). Copy enforced terms exactly, with their
accents, the apostrophe ’ and German „“ quotes. If a correct translation can't contain an
enforced term, don't bend the sentence to pass: tell the owner, and change the glossary.

## Voice

- The English is plain, short and friendly. It talks to one person, says what happened and what
  to do next, and never blames the reader. Keep that tone.
- Translate the meaning, not the words. Keep every fact, limit and consequence; add nothing and
  drop nothing.
- No marketing tone, and no exclamation marks or "please" that the English doesn't have.
- Emoji in the English (✅, ⚠️, 🎉) stay where they are.
- The English spells the British way (recognise, centre). That says nothing about the
  translation.
- Texts marked `"review": "required"` carry consent, legal, security or destructive meaning.
  Never soften them: "can't be undone", "is erased" and "can't be recovered" keep their full
  force.

**Address**: informal in Spanish (tú), German (du), Italian (tu), Portuguese (tu) and Dutch
(je); formal in French (vous). Always one person, in the singular.

## Product words

- **device** is the Off The Cloud box: a Raspberry Pi with its disks. App text never calls it a
  server, a NAS or a box.
- Groups of images are always **collections**, never albums: every app, the site and the store
  texts. Each language forbids its word for album.
- **Images** is the tab. Inside sentences the apps say "photos and videos". **Photos** is
  Apple's app. In the setup texts, **image** (singular) is the system image written to the
  microSD card. Keep the four apart.
- **Alerts** is the tab: the device's alerts and friends' activity. **Notifications** are the
  pushes on phones and in the browser. They are different words in every language.
- **bridge** is our relay at off-the.cloud. It is one fixed word per language, lowercase inside a
  sentence (German capitalizes it, as a noun).
- **account** can be the off-the.cloud account, an account on the device (a user, in Settings,
  Users), or a Tailscale, Apple or Google account. **user** is a person with an account on the
  device.
- **Upload only** and **Keep out of Images** are names of folder options. The web, phone, Mac and
  Windows/Linux apps all use them, so they are the same words everywhere.
- The Mac app says **this Mac** and the Windows/Linux app says **this computer**. Apart from that
  word, the two apps' texts are identical, and the translations must be too.
- When a text names a tab, button or option ("choose Add to collection"), use that label's
  translation exactly. The glossary lists the labels other texts quote. Put a label in the
  language's quotes when it is not a single capitalized name.

## Never translated

These names appear exactly as written: Off The Cloud, off-the.cloud, OTC, otc-sync, Tailscale,
Tailscale Funnel, Funnel, tailnet, ts.net, GitHub, Google, Google Play, Apple, App Store, Mac App
Store, iCloud, iPhone, iPad, iOS, Mac, macOS, Finder, Android, Windows, Linux, Ubuntu, Debian,
Raspberry Pi, Raspberry Pi Imager, Orange Pi, Bluetooth, USB, USB-C, microSD, SD, RAID, ZIP, EXIF,
PDF, HTTP, VPN, NAS, RAM, ARM, ARM64, GPIO, GND, MariaDB, ONNX, Proton, README.

A few things are left out of that list on purpose, because some languages write them
differently: Wi-Fi (German WLAN, Dutch wifi), CPU (French processeur), LED and PC (Dutch led and
pc), Ethernet (Dutch ethernetkabel), GB and MB (French Go and Mo), AI (German KI, IA in the
Romance languages).

Don't fold a name into a compound that changes it. German Off-The-Cloud-App fails the check:
write die App Off The Cloud, or just die App. Compounds that leave the name whole are fine:
Tailscale-Konto, iCloud-Fotos, USB-Anschluss, Off The Cloud-app (Dutch).

The check can't see these, but they stay English too:

- **Protocol words and values.** A typed confirmation word reaches the text as a placeholder, so
  keep the placeholder (`Type {word} to confirm`). Each language section suggests a word.
- **Data.** File names the apps write (conflict copies, `.otc-part`, zips), the device's Wi-Fi
  name and `OTC <id>`, image tags and place names (they are search keys), and release summaries.
- **Text from elsewhere.** Text from friends' devices, and a device's English error detail.
  These arrive as arguments, after a sentence you translate.

Also staying English, and never sent for translation: the privacy policy, the terms, store
listings and screenshots, log lines, and the otc-sync command line (its tray menu is translated).

## Placeholders, tags and plurals

- Keep each `{name}` exactly as it is, and move it wherever the grammar needs it. Never translate
  the word inside the braces, and never add or drop a placeholder. The apps insert arguments as
  plain text.
- People's names (argument type `user`) get isolation marks at runtime. Don't add quotes around a
  name unless the English has them.
- Tags such as `<link>…</link>` and `<b>…</b>`: keep the same tags, translate the words inside,
  and move each tag with the words it belongs to. A tag with no matching tag in the English fails
  the check.
- Plurals have exactly two forms, `one` and `other`:
  - `one` means exactly 1 in es, de, it, nl and pt (European rules: 0 takes the plural).
  - In French, `one` covers 0 and 1, so the French `one` form always contains `{count}`.
  - Elsewhere `one` may leave the number out ("una foto").
  - Each form agrees in full: verb, article and adjective.
  - Exact millions in es, fr, it and pt use `other` ("1.000.000 fotos", without "de"). This is a
    known limitation; don't work around it.
- Never write a sentence that depends on what a placeholder holds:
  - Gender: put the noun before the name ("la carpeta “{name}”", "le fichier {name}"), so
    articles and adjectives agree with a noun you know.
  - First letter: Spanish y/e and o/u, and French and Italian elision. Write "de {name}", never
    "d’{name}".
- The apps format numbers, dates and sizes in the reader's language. Never write digits,
  separators, units or month names around a placeholder. The one exception is the % sign after a
  number placeholder: the English writes it, so you write it in your language's style.

## Typography

- **Ellipsis**: one character, … (U+2026), never three dots. Keep it wherever the English has it:
  a busy state, or a button that opens a dialog.
- **Apostrophe**: ’ (U+2019), never '. Examples: foto’s, l’app, J’aime, d’Images.
- **Quotes**: Spanish “…”, French « … » (with no-break spaces inside), German „…“, Italian “…”,
  Portuguese «…», Dutch “…”. Never straight quotes (").
- **Dashes**: the English uses " - " (a hyphen between spaces) as a dash. Rewrite it as a colon,
  comma or parentheses where that reads better; otherwise use an en dash with spaces: " – ".
- **Spaces**: no double spaces, no space at either end, no tabs, no soft hyphens or zero-width
  characters. Break lines only where the English does.
- A **no-break space** (U+00A0) goes only where a language section asks for one (French
  punctuation; a number before % or a unit, in text you write yourself).
- Put a space after every full stop. The check reads "a.b" written without a space as a domain
  name, and domains must come from the English.

## Capitals

English labels are in Title Case ("Change Password", "Reprocess All Media"). Every target
language uses sentence case for labels, buttons, titles and menu items: only the first word and
proper nouns take a capital, and German also capitalizes every noun. Tab names and other named
sections keep their capital inside a sentence (en Imágenes, dans Images).

## Numbers, units and dates

| | decimal | thousands | percent | size | price | time | date |
|---|---|---|---|---|---|---|---|
| es | 1,5 | 12.345 | 45 % | 1,5 GB | 9,99 € | 19:34 | 5 de octubre de 2026 |
| fr | 1,5 | 12 345 | 45 % | 1,5 Go | 9,99 € | 19:34 | 5 octobre 2026 |
| de | 1,5 | 12.345 | 45 % | 1,5 GB | 9,99 € | 19:34 | 5. Oktober 2026 |
| it | 1,5 | 12.345 | 45% | 1,5 GB | 9,99 € | 19:34 | 5 ottobre 2026 |
| pt | 1,5 | 12 345 | 45% | 1,5 GB | 9,99 € | 19:34 | 5 de outubro de 2026 |
| nl | 1,5 | 12.345 | 45% | 1,5 GB | € 9,99 | 19:34 | 5 oktober 2026 |

- The space in "45 %", "1,5 GB" and "9,99 €" is a no-break space. The apps format French with a
  narrow one (U+202F); where you write it yourself, U+00A0 is fine.
- French counts bytes in octets: o, Ko, Mo, Go, To, and Mo/s. The other languages write KB, MB,
  GB and TB.
- Every language uses the 24-hour clock.
- Months and weekdays are lowercase, except in German.
- es and pt don't separate a four-digit number (1234).

## Buttons, titles and messages

| English | es | fr | de | it | pt | nl |
|---|---|---|---|---|---|---|
| Change Password | Cambiar contraseña | Modifier le mot de passe | Passwort ändern | Modifica password | Alterar palavra-passe | Wachtwoord wijzigen |
| Delete this post? | ¿Eliminar esta publicación? | Supprimer cette publication ? | Diesen Beitrag löschen? | Eliminare questo post? | Eliminar esta publicação? | Dit bericht verwijderen? |
| Saving… | Guardando… | Enregistrement… | Wird gespeichert… | Salvataggio… | A guardar… | Opslaan… |
| Password changed | Contraseña cambiada | Mot de passe modifié | Passwort geändert | Password modificata | Palavra-passe alterada | Wachtwoord gewijzigd |
| No collections yet | Aún no hay colecciones | Aucune collection pour l’instant | Noch keine Sammlungen | Ancora nessuna raccolta | Ainda não há coleções | Nog geen collecties |
| Could not delete the collection. | No se ha podido eliminar la colección. | Impossible de supprimer la collection. | Die Sammlung konnte nicht gelöscht werden. | Impossibile eliminare la raccolta. | Não foi possível eliminar a coleção. | Kan de collectie niet verwijderen. |
| Try again. | Vuelve a intentarlo. | Réessayez. | Versuche es erneut. | Riprova. | Tenta novamente. | Probeer het opnieuw. |
| Could not save: {detail} | No se ha podido guardar: {detail} | Impossible d’enregistrer : {detail} | Speichern fehlgeschlagen: {detail} | Impossibile salvare: {detail} | Não foi possível guardar: {detail} | Opslaan mislukt: {detail} |
| {name} sent you a friend request | {name} te ha enviado una solicitud de amistad | {name} vous a envoyé une demande d’ami | {name} hat dir eine Freundschaftsanfrage gesendet | {name} ti ha inviato una richiesta di amicizia | {name} enviou-te um pedido de amizade | {name} heeft je een vriendschapsverzoek gestuurd |
| {name} liked your post | A {name} le ha gustado tu publicación | {name} a aimé votre publication | {name} gefällt dein Beitrag | A {name} piace il tuo post | {name} gostou da tua publicação | {name} vindt je bericht leuk |

- **Buttons and menu items**: the verb alone where the language allows, with no article and no
  "please". The form is the infinitive in es, fr, de, pt and nl (German puts it last). Italian
  uses the imperative, as Apple and Google do (Salva, Elimina).
- **Alert titles** (the device's alerts) are short sentences with no final full stop. The details
  are full sentences, with full stops.
- **Confirmations**: the question names both the action and the thing. The button repeats the
  verb (Eliminar, not Sí or Aceptar).
- **After something worked**: a short past-participle phrase, as in the English.
- **Busy states**: the language's progressive form or a noun, then … (see each language).
- **Errors** say what failed, then what to do. Keep "Try again" only where the English has it.
- **A device's English detail** comes as an argument after a colon. Write the lead sentence so
  that a colon and an English sentence can follow it.
- **Push notifications** may use only `{name}`, `{version}` and `{port}`. Keep them short: a lock
  screen shows about two lines. The name may go anywhere in the sentence.

## Length

- Buttons and labels: stay within about 1.3 times the English. German and French run about 30%
  longer, so on a button pick the shorter of two good words.
- Tight spots: the web menu's 80 px rail and the phones' tab bar (labels of 12 characters at
  most; German Einstellungen is 13, accepted, and checked on screenshots), the selection bars, the
  Mac popover (360 pt wide), the buttons in the Windows folder picker (150 and 100 px, about 18
  and 12 characters), the Windows tray tooltip (128 UTF-16 units, "Off The Cloud — " included)
  and push notifications.
- A key with a `max` fails the check when a translation is longer than that.
- Never abbreviate to fit (no "Config.", no "Einst."). Find a shorter word, or tell the owner.

## Other products' words

- System screens, paths and buttons (Settings › Privacy & Security › Bluetooth, Control Centre,
  Login Items, Trash) use the operating system's own words in that language. The platform table
  below has them; where a note says to check, check on a device in that language.
- The Mac setup quotes Raspberry Pi Imager's buttons (Choose Device, Choose OS, Use custom, Choose
  Storage, Next, "apply OS customisation settings"). Imager is translated, so use Imager's own
  words in that language.
- Apple and Google publish their localized sign-in buttons (Continue with Apple, Continue with
  Google) and store badges. Use their wording when it differs from the table.
- System UI the apps can't change stays in the phone's language, not the app's: permission
  prompts, share sheets, file pickers.

## Spanish (es)

- **Variant**: Spanish as written in Spain, like Apple's and Google's Spanish for Spain; numbers
  and dates follow the `es` locale. Where a neutral word exists, prefer it (teléfono rather than
  móvil), but keep Spain's ordenador, vídeo and añadir. Solo and este take no accent.
- **Address**: tú, in the singular: elige, toca, escribe, vuelve a intentarlo. Tu (your) has no
  accent; tú (you) has one. Never usted.
- **Tense**: the perfect for what just happened, as in Spain: Se ha cambiado la contraseña, No se
  ha podido guardar.
- **Capitals**: sentence case (Cambiar contraseña, Reprocesar multimedia). Days, months and
  languages are lowercase.
- **Punctuation**: ¿…? and ¡…! open every question and exclamation, even in mid-sentence (Si no
  aparece, ¿está encendido?). Quotes “…”. No space before : ; ? !, but a no-break space before %
  (45 %). No comma before y in a list (fotos, vídeos y archivos).
- **Placeholders**: put the noun before a name (la carpeta “{name}”, el archivo {name}). With a
  person's name, avoid words that agree with the person: {name} te ha enviado una solicitud de
  amistad, not {name} está conectado. The like alert puts the name inside: A {name} le ha gustado
  tu publicación. Y and o before a placeholder can't change to e and u: rephrase, or let the list
  formatter join the words.
- **Busy**: gerund and …: Guardando…, Subiendo 3 de 10…
- **Errors**: No se ha podido {infinitive}. Vuelve a intentarlo.
- **Platform**: Settings is Ajustes on iOS and on Android in Spain (Windows says Configuración).
  Delete is Eliminar; Borrar means erase (se borrarán los discos). iOS writes Done as OK; we
  write Hecho.
- **Typed confirmation word**: eliminar, the same verb as the button. The owner has to confirm
  this: docs/i18n.md gives borrar as its example.
- **Length**: about 25% longer than English. Colecciones (11 characters) is the longest tab.

## French (fr)

- **Variant**: French as written in France.
- **Address**: vous, votre and vos, with the vous imperative (Choisissez, Touchez, Réessayez).
  Never tu.
- **Capitals**: sentence case, and no capital after a colon. Tab names keep their capital.
- **Punctuation**:
  - A no-break space (U+00A0) goes before : ; ? ! and before % and €, and inside « »
    (« Vacances », Supprimer ce dossier ?, 45 %).
  - No space before …, a comma or a full stop, and none after an opening parenthesis.
  - Apostrophe ’. Asides go between spaced en dashes.
- **Numbers**: decimal comma. Sizes are in octets: Ko, Mo, Go, To, and Mo/s.
- **Plurals**: 0 and 1 take the singular (0 photo, 1 photo, 2 photos), so the `one` form always
  contains {count}.
- **Placeholders**:
  - Put the noun before a name (le dossier « {name} », la collection « {name} »).
  - Never elide before a placeholder: de {name}, not d’{name}.
  - With a person's name, use verbs conjugated with avoir, so that nothing agrees with the
    person: {name} a accepté votre demande d’ami, not {name} est connecté. No inclusive spellings
    (connecté·e).
- **Vocabulary**: avoid anglicisms where French interfaces have a word: envoyer (upload),
  télécharger (download), passerelle (bridge), chiffré (encrypted). E-mail is fine, as Apple
  writes it.
- **Settings**: our tab is Paramètres, as on Android and Windows. A text that sends the reader to
  the iPhone's settings says Réglages, and Réglages Système on a Mac.
- **Busy**: a noun and … (Enregistrement…, Chargement…, Envoi de 3 sur 10…).
- **Errors**: Impossible de {infinitive}. Réessayez.
- **Typed confirmation word**: supprimer.
- **Length**: 20 to 30% longer than English.

## German (de)

- **Address**: du, with du, dich, dir and dein lowercase except at the start of a sentence. The
  imperative uses du (Wähle, Tippe auf, Versuche es erneut). Buttons and menu items put the
  infinitive last (Passwort ändern, Gerät neu starten).
- **Capitals**: every noun; otherwise sentence case.
- **Compounds**: one word or hyphenated, never split with a space (Speicherplatz, WLAN-Netz,
  USB-Anschluss, ZIP-Datei, Tailscale-Konto, Bridge-Zugang). See "Never translated" for names
  inside compounds.
- **Punctuation**: quotes „…“. The ellipsis goes right after the word. Asides go between spaced en
  dashes. No space before ? ! :, but a no-break space before % (45 %).
  Dates are written 5. Oktober 2026. Avoid abbreviations such as z. B. in interface text.
- **Tab names in a sentence**: where the sentence would decline the name (den Bildern, den
  Freunden), quote it unchanged instead: in „Bilder“, unter „Freunde“. Names that don't change
  (in den Einstellungen, in Dateien) need no quotes.
- **Placeholders**: case matters, so put the noun before a name and let the article belong to
  the noun (der Ordner „{name}“, die Sammlung „{name}“). Never put Freund or Freundin next to a
  name, and use no gender star or colon (Freund:innen). Prefer Personen, alle, die …, or Freunde.
- **Busy**: Wird gespeichert…, Wird geladen…, Wird hochgeladen (3 von 10)…
- **Errors**: Die Sammlung konnte nicht gelöscht werden. Versuche es erneut. Short form:
  Speichern fehlgeschlagen.
- **Platform**:
  - Apple's settings call notifications Mitteilungen; Android and Windows say
    Benachrichtigungen. We write Benachrichtigungen, except when sending the reader to the
    iPhone's settings.
  - Wi-Fi is always WLAN.
- **Typed confirmation word**: löschen.
- **Length**: 30 to 35% longer than English. Einstellungen (13 characters) is the longest tab.

## Italian (it)

- **Address**: tu (Scegli, Tocca, Riprova; il tuo, la tua). Buttons use the imperative, as Apple
  and Google write them: Salva, Elimina, Annulla, Condividi.
- **Loanwords**: Italian interfaces keep these, invariable: il file and i file, la password,
  l’account, il backup, il link, il post, il log, il bridge, l’app.
- **Capitals**: sentence case.
- **Punctuation**: quotes “…”; apostrophe ’ (un’app, l’account). È takes its accent as a
  capital, never E’; perché and poiché take é. Ellipsis …; no space before ? !.
- **Numbers**: 45%, with no space.
- **Placeholders**: put the noun first (la cartella “{name}”, la raccolta “{name}”). With a
  person's name, avoid essere with a participle (è stato or è stata); use avere instead: {name}
  ha accettato la tua richiesta di amicizia. Likes: A {name} piace il tuo post.
- **Questions** use the infinitive: Eliminare questo post?
- **Busy**: Salvataggio…, Caricamento…
- **Errors**: Impossibile {infinitive}. Riprova.
- **Typed confirmation word**: elimina.
- **Length**: 20 to 25% longer than English.

## Portuguese (pt)

- **Variant**: European Portuguese (pt-PT), in the spelling of the 1990 agreement (atualização,
  ação, direção, receção; facto and contacto keep their c). Brazilians get this language too, and
  that is accepted, but never mix in Brazilian forms; the forbidden list catches the common ones.
  Formats follow pt-PT, where 0 takes the plural.
- **Address**: tu (Escolhe, Toca, Tenta novamente; o teu, a tua), with object pronouns after the
  verb and a hyphen (enviou-te, liga-te). Never você. Buttons use the infinitive: Guardar,
  Eliminar, Partilhar.
- **Continuous forms**: a with the infinitive, never the gerund: A carregar…, A guardar…, A
  transferir 3 de 10…
- **Vocabulary**: ecrã, ficheiro, utilizador, telemóvel, transferir (download), carregar
  (upload), guardar (save), partilhar, palavra-passe, definições, registo, equipa, app or
  aplicação.
- **Capitals**: sentence case; months are lowercase (outubro).
- **Punctuation**: quotes «…»; ellipsis …; no space before ? ! :.
- **Placeholders**: put the noun first (a pasta «{name}», o ficheiro {name}). Nothing should
  agree with a person's name: {name} enviou-te um pedido de amizade.
- **Delete** is Eliminar. Apple writes Apagar (also European), but we use Eliminar. Never write
  excluir: it means delete in Brazil and exclude in Portugal.
- **Errors**: Não foi possível {infinitive}. Tenta novamente.
- **Typed confirmation word**: eliminar.
- **Length**: 20 to 30% longer than English.

## Dutch (nl)

- **Address**: je, with jouw and jij when stressed (Probeer het opnieuw, Kies, Tik op). Never u
  or uw. Buttons use the infinitive (Opslaan, Annuleren, Verwijderen), as Google and Microsoft
  write them; use Apple's imperative (Bewaar) only when quoting Apple.
- **Capitals**: sentence case.
- **Compounds**:
  - Written closed: wachtwoord, thuisnetwerk, configuratiecode, wifinetwerk, deellink.
  - A hyphen after abbreviations and names: USB-poort, SD-kaart, ZIP-bestand, Off The
    Cloud-app, Tailscale-account.
  - USB, SD, ZIP, RAID and Bluetooth keep their capitals as Apple writes them (they are on the
    never-translate list), even where Dutch spelling would lowercase them. Wifi is lowercase.
- **Punctuation**: quotes “…”; apostrophe ’ (foto’s, video’s); ellipsis …; no space before ? ! :.
- **Numbers**: 45%, € 9,99.
- **Placeholders**: put the de or het noun first (de map “{name}”, het bestand {name}, de
  collectie “{name}”).
- **Alerts and notifications**: the Alerts tab is Meldingen, and push notifications are
  pushmeldingen.
- **Busy**: Opslaan…, Laden…, Uploaden (3 van 10)…
- **Errors**: Kan de collectie niet verwijderen. Probeer het opnieuw. Short form: Opslaan mislukt.
- **Typed confirmation word**: verwijderen.
- **Length**: 25 to 30% longer than English. Afbeeldingen and Instellingen (12 characters each)
  are the longest tabs.

## Product terms

Copied from `<code>.json`. **Bold** terms are enforced in that language. The notes in the JSON
files give gender, plural and usage.

| English | es | fr | de | it | pt | nl |
|---|---|---|---|---|---|---|
| Images | **Imágenes** | **Images** | **Bilder** | **Immagini** | **Imagens** | **Afbeeldingen** |
| image | imagen | image | Image | immagine | imagem | image |
| People | Personas | Personnes | Personen | Persone | Pessoas | Personen |
| Collections | **Colecciones** | **Collections** | **Sammlungen** | **Raccolte** | **Coleções** | **Collecties** |
| collection | **colección** | **collection** | **Sammlung** | **raccolta** | **coleção** | **collectie** |
| Add to collection | **Añadir a una colección** | **Ajouter à une collection** | **Zur Sammlung hinzufügen** | **Aggiungi a una raccolta** | **Adicionar a uma coleção** | **Toevoegen aan collectie** |
| Files | **Archivos** | **Fichiers** | **Dateien** | **File** | **Ficheiros** | **Bestanden** |
| file | archivo | fichier | Datei | file | ficheiro | bestand |
| folder | **carpeta** | **dossier** | **Ordner** | **cartella** | **pasta** | **map** |
| folders | **carpetas** | **dossiers** | **Ordner** | **cartelle** | **pastas** | **mappen** |
| Social | **Social** | **Social** | Social | **Social** | **Social** | Social |
| Friends | **Amigos** | **Amis** | **Freunde** | **Amici** | **Amigos** | **Vrienden** |
| friend | amigo | ami | Freund | amico | amigo | vriend |
| Alerts | **Alertas** | **Alertes** | **Hinweise** | **Avvisi** | **Alertas** | **Meldingen** |
| Settings | **Ajustes** | Paramètres | **Einstellungen** | **Impostazioni** | **Definições** | **Instellingen** |
| Sharing | Compartir | Partage | Teilen | Condivisione | Partilha | Delen |
| device | **dispositivo** | **appareil** | **Gerät** | **dispositivo** | **dispositivo** | **apparaat** |
| devices | **dispositivos** | **appareils** | **Geräte** | **dispositivi** | **dispositivos** | **apparaten** |
| bridge | **puente** | **passerelle** | **Bridge** | **bridge** | **ponte** | **bridge** |
| bridge access | **acceso al puente** | **accès à la passerelle** | **Bridge-Zugang** | **accesso al bridge** | **acesso à ponte** | **toegang tot de bridge** |
| Request bridge access | **Solicitar acceso al puente** | **Demander l’accès à la passerelle** | **Bridge-Zugang anfordern** | **Richiedi l’accesso al bridge** | **Pedir acesso à ponte** | **Toegang tot de bridge aanvragen** |
| account | **cuenta** | **compte** | **Konto** | **account** | **conta** | **account** |
| accounts | **cuentas** | **comptes** | **Konten** | **account** | **contas** | **accounts** |
| user | **usuario** | **utilisateur** | **Benutzer** | **utente** | **utilizador** | **gebruiker** |
| users | **usuarios** | **utilisateurs** | **Benutzer** | **utenti** | **utilizadores** | **gebruikers** |
| owner | propietario | propriétaire | Besitzer | proprietario | proprietário | eigenaar |
| password | **contraseña** | **mot de passe** | **Passwort** | **password** | **palavra-passe** | **wachtwoord** |
| passwords | **contraseñas** | **mots de passe** | **Passwörter** | **password** | **palavras-passe** | **wachtwoorden** |
| setup code | **código de configuración** | **code de configuration** | **Einrichtungscode** | **codice di configurazione** | **código de configuração** | **configuratiecode** |
| setup codes | **códigos de configuración** | **codes de configuration** | **Einrichtungscodes** | **codici di configurazione** | **códigos de configuração** | **configuratiecodes** |
| I have a setup code | **Tengo un código de configuración** | **J’ai un code de configuration** | **Ich habe einen Einrichtungscode** | **Ho un codice di configurazione** | **Tenho um código de configuração** | **Ik heb een configuratiecode** |
| set up | configurar | configurer | einrichten | configurare | configurar | instellen |
| Set up a new device | Configurar un dispositivo nuevo | Configurer un nouvel appareil | Neues Gerät einrichten | Configura un nuovo dispositivo | Configurar um novo dispositivo | Nieuw apparaat instellen |
| recover | recuperar | récupérer | wiederherstellen | recuperare | recuperar | herstellen |
| Recover this device | **Recuperar este dispositivo** | **Récupérer cet appareil** | **Dieses Gerät wiederherstellen** | **Recupera questo dispositivo** | **Recuperar este dispositivo** | **Dit apparaat herstellen** |
| Start fresh instead | **Empezar de cero** | **Repartir de zéro** | **Neu beginnen** | **Ricomincia da zero** | **Começar do zero** | **Opnieuw beginnen** |
| Blink its light | **Hacer parpadear la luz** | **Faire clignoter le voyant** | **Licht blinken lassen** | **Fai lampeggiare la luce** | **Fazer piscar a luz** | **Lampje laten knipperen** |
| storage | almacenamiento | stockage | Speicher | spazio di archiviazione | armazenamento | opslag |
| disk | disco | disque | Laufwerk | disco | disco | schijf |
| mirror | **espejo** | **miroir** | Spiegelung | **mirror** | **espelho** | **spiegel** |
| sync | sincronizar | synchroniser | synchronisieren | sincronizzare | sincronizar | synchroniseren |
| two-way | bidireccional | bidirectionnel | in beide Richtungen | bidirezionale | bidirecional | in twee richtingen |
| backup | copia de seguridad | sauvegarde | Backup | backup | cópia de segurança | reservekopie |
| upload | subir | envoyer | hochladen | caricare | carregar | uploaden |
| download | descargar | télécharger | herunterladen | scaricare | transferir | downloaden |
| upload only | **solo subida** | **envoi uniquement** | **Nur hochladen** | **solo caricamento** | **apenas carregamento** | **alleen uploaden** |
| upload-only | **solo subida** | **envoi uniquement** | **Nur hochladen** | **solo caricamento** | **apenas carregamento** | **alleen uploaden** |
| Keep out of Images | **Excluir de Imágenes** | **Exclure d’Images** | **In „Bilder“ ausblenden** | **Escludi da Immagini** | **Manter fora de Imagens** | **Buiten Afbeeldingen houden** |
| Kept out of Images | **Fuera de Imágenes** | **Hors d’Images** | **In „Bilder“ ausgeblendet** | **Fuori da Immagini** | **Fora de Imagens** | **Buiten Afbeeldingen** |
| Show in Images | **Mostrar en Imágenes** | **Afficher dans Images** | **In „Bilder“ anzeigen** | **Mostra in Immagini** | **Mostrar em Imagens** | **In Afbeeldingen tonen** |
| share link | enlace para compartir | lien de partage | Freigabelink | link di condivisione | ligação de partilha | deellink |
| Shared Links | **Enlaces compartidos** | **Liens partagés** | Geteilte Links | **Link condivisi** | **Ligações partilhadas** | **Gedeelde links** |
| shared gallery | **galería compartida** | **galerie partagée** | geteilte Galerie | **galleria condivisa** | **galeria partilhada** | **gedeelde galerij** |
| Share as Gallery | **Compartir como galería** | **Partager en galerie** | **Als Galerie teilen** | **Condividi come galleria** | **Partilhar como galeria** | **Delen als galerij** |
| post | publicación | publication | Beitrag | post | publicação | bericht |
| posts | **publicaciones** | **publications** | **Beiträge** | **post** | **publicações** | **berichten** |
| like | Me gusta | J’aime | Gefällt mir | Mi piace | Gosto | Vind ik leuk |
| comment | comentario | commentaire | Kommentar | commento | comentário | reactie |
| caption | pie de foto | légende | Bildunterschrift | didascalia | legenda | bijschrift |
| friend request | **solicitud de amistad** | **demande d’ami** | **Freundschaftsanfrage** | **richiesta di amicizia** | **pedido de amizade** | **vriendschapsverzoek** |
| friend requests | **solicitudes de amistad** | **demandes d’ami** | **Freundschaftsanfragen** | **richieste di amicizia** | **pedidos de amizade** | **vriendschapsverzoeken** |
| Accept | Aceptar | Accepter | Annehmen | Accetta | Aceitar | Accepteren |
| Decline | Rechazar | Refuser | Ablehnen | Rifiuta | Recusar | Weigeren |
| Block | Bloquear | Bloquer | Blockieren | Blocca | Bloquear | Blokkeren |
| face recognition | **reconocimiento facial** | **reconnaissance faciale** | **Gesichtserkennung** | **riconoscimento facciale** | **reconhecimento facial** | **gezichtsherkenning** |
| image tagging | **etiquetado de imágenes** | **étiquetage des images** | **Bilderkennung** | **etichettatura delle immagini** | **etiquetagem de imagens** | **beeldherkenning** |
| photo tagging | etiquetado de imágenes | étiquetage des images | Bilderkennung | etichettatura delle immagini | etiquetagem de imagens | beeldherkenning |
| tags | **etiquetas** | **étiquettes** | **Tags** | **etichette** | **etiquetas** | **tags** |
| tag | etiqueta | étiquette | Tag | etichetta | etiqueta | tag |
| Reprocess Media | **Reprocesar multimedia** | **Retraiter les médias** | **Medien neu verarbeiten** | **Rielabora media** | **Reprocessar multimédia** | **Media opnieuw verwerken** |
| set aside | apartado | mis de côté | zurückgestellt | messo da parte | posto de parte | apart gezet |
| Restart Device | Reiniciar dispositivo | Redémarrer l’appareil | Gerät neu starten | Riavvia dispositivo | Reiniciar dispositivo | Apparaat herstarten |
| update | actualización | mise à jour | Update | aggiornamento | atualização | update |
| critical update | **actualización crítica** | **mise à jour critique** | kritisches Update | **aggiornamento critico** | **atualização crítica** | **kritieke update** |
| up to date | actualizado | à jour | auf dem neuesten Stand | aggiornato | atualizado | up-to-date |
| sign in | iniciar sesión | se connecter | anmelden | accedere | iniciar sessão | inloggen |
| sign out | cerrar sesión | se déconnecter | abmelden | uscire | terminar sessão | uitloggen |
| log out | cerrar sesión | se déconnecter | abmelden | uscire | terminar sessão | uitloggen |
| disconnect | desconectar | déconnecter | trennen | disconnettere | desligar | loskoppelen |
| Wi-Fi | **Wi-Fi** | **Wi-Fi** | **WLAN** | **Wi-Fi** | **Wi-Fi** | **wifi** |
| WiFi | **Wi-Fi** | **Wi-Fi** | **WLAN** | **Wi-Fi** | **Wi-Fi** | **wifi** |
| home network | **red doméstica** | **réseau domestique** | **Heimnetz** | **rete domestica** | **rede doméstica** | **thuisnetwerk** |
| local network | red local | réseau local | lokales Netzwerk | rete locale | rede local | lokaal netwerk |
| mobile data | datos móviles | données mobiles | mobile Daten | dati mobili | dados móveis | mobiele data |
| hotspot | punto de acceso | point d’accès | Hotspot | hotspot | ponto de acesso | hotspot |
| address | dirección | adresse | Adresse | indirizzo | endereço | adres |
| domain | dominio | domaine | Domain | dominio | domínio | domein |
| notifications | **notificaciones** | **notifications** | Benachrichtigungen | **notifiche** | **notificações** | pushmeldingen |
| logs | **registros** | **journaux** | **Protokolle** | **log** | **registos** | **logboeken** |
| profile | **perfil** | **profil** | **Profil** | **profilo** | **perfil** | **profiel** |
| profile picture | foto de perfil | photo de profil | Profilbild | foto del profilo | foto de perfil | profielfoto |
| library | biblioteca | bibliothèque | Mediathek | libreria | biblioteca | bibliotheek |
| thumbnail | miniatura | vignette | Miniatur | miniatura | miniatura | miniatuur |
| photo | foto | photo | Foto | foto | foto | foto |
| video | **vídeo** | vidéo | Video | video | **vídeo** | video |
| videos | **vídeos** | vidéos | Videos | video | **vídeos** | video’s |
| phone | teléfono | téléphone | Handy | telefono | **telemóvel** | telefoon |
| phones | teléfonos | téléphones | Handys | telefoni | **telemóveis** | telefoons |
| computer | **ordenador** | ordinateur | Computer | computer | computador | computer |
| computers | **ordenadores** | ordinateurs | Computer | computer | computadores | computers |
| screen | pantalla | écran | Bildschirm | schermo | **ecrã** | scherm |
| app | app | app | App | app | app | app |
| web app | app web | app web | Web-App | app web | app web | webapp |
| this Mac | este Mac | ce Mac | dieser Mac | questo Mac | este Mac | deze Mac |
| this computer | este ordenador | cet ordinateur | dieser Computer | questo computer | este computador | deze computer |
| encrypted | cifrado | chiffré | verschlüsselt | crittografato | encriptado | versleuteld |
| key | clave | clé | Schlüssel | chiave | chave | sleutel |
| version | versión | version | Version | versione | versão | versie |
| conflict | conflicto | conflit | Konflikt | conflitto | conflito | conflict |
| blue USB port | puerto USB azul | port USB bleu | blauer USB-Anschluss | porta USB blu | porta USB azul | blauwe USB-poort |
| Download ZIP | **Descargar ZIP** | **Télécharger en ZIP** | **ZIP herunterladen** | **Scarica ZIP** | **Transferir ZIP** | **ZIP downloaden** |
| Sync All | **Sincronizar todo** | **Tout synchroniser** | **Alles synchronisieren** | **Sincronizza tutto** | **Sincronizar tudo** | **Alles synchroniseren** |
| Sync From Now | **Sincronizar desde ahora** | **Synchroniser dès maintenant** | **Ab jetzt synchronisieren** | **Sincronizza da ora** | **Sincronizar a partir de agora** | **Vanaf nu synchroniseren** |
| Send to us | **Enviarnos** | **Nous envoyer** | **An uns senden** | **Inviaci** | **Enviar-nos** | **Naar ons sturen** |
| Save & Continue | **Guardar y continuar** | **Enregistrer et continuer** | **Speichern und fortfahren** | **Salva e continua** | **Guardar e continuar** | **Opslaan en doorgaan** |
| Start at Login | Abrir al iniciar sesión | Ouvrir avec la session | Bei der Anmeldung öffnen | Apri al login | Abrir ao iniciar sessão | Openen bij inloggen |
| AI | IA | IA | KI | IA | IA | AI |
| VAT | IVA | TVA | MwSt. | IVA | IVA | btw |

## Platform terms

What iOS, macOS, Android and Windows call things in each language, so that our text matches the
system around it. These are never enforced. The JSON notes say where the platforms disagree;
names marked "check" in the notes must be confirmed on a device in that language before
shipping.

| English | es | fr | de | it | pt | nl |
|---|---|---|---|---|---|---|
| Cancel | Cancelar | Annuler | Abbrechen | Annulla | Cancelar | Annuleren |
| Delete | Eliminar | Supprimer | Löschen | Elimina | Eliminar | Verwijderen |
| Remove | Quitar | Retirer | Entfernen | Rimuovi | Remover | Verwijderen |
| Share | Compartir | Partager | Teilen | Condividi | Partilhar | Delen |
| Save | Guardar | Enregistrer | Speichern | Salva | Guardar | Opslaan |
| Done | Hecho | OK | Fertig | Fine | Concluído | Klaar |
| OK | Aceptar | OK | OK | OK | OK | OK |
| Edit | Editar | Modifier | Bearbeiten | Modifica | Editar | Bewerken |
| Rename | Renombrar | Renommer | Umbenennen | Rinomina | Mudar o nome | Naam wijzigen |
| Close | Cerrar | Fermer | Schließen | Chiudi | Fechar | Sluiten |
| Back | Atrás | Retour | Zurück | Indietro | Voltar | Terug |
| Next | Siguiente | Suivant | Weiter | Avanti | Seguinte | Volgende |
| Continue | Continuar | Continuer | Weiter | Continua | Continuar | Doorgaan |
| Search | Buscar | Rechercher | Suchen | Cerca | Pesquisar | Zoeken |
| Copy | Copiar | Copier | Kopieren | Copia | Copiar | Kopiëren |
| Copy Link | Copiar enlace | Copier le lien | Link kopieren | Copia link | Copiar ligação | Link kopiëren |
| Open | Abrir | Ouvrir | Öffnen | Apri | Abrir | Openen |
| Select | Seleccionar | Sélectionner | Auswählen | Seleziona | Selecionar | Selecteren |
| Try Again | Reintentar | Réessayer | Erneut versuchen | Riprova | Tentar novamente | Opnieuw proberen |
| Not Now | Ahora no | Plus tard | Nicht jetzt | Non ora | Agora não | Niet nu |
| Allow | Permitir | Autoriser | Erlauben | Consenti | Permitir | Toestaan |
| Turn On | Activar | Activer | Einschalten | Attiva | Ativar | Inschakelen |
| Quit | Salir de Off The Cloud | Quitter Off The Cloud | Off The Cloud beenden | Esci da Off The Cloud | Sair de Off The Cloud | Stop Off The Cloud |
| Continue with Apple | Continuar con Apple | Continuer avec Apple | Mit Apple fortfahren | Continua con Apple | Continuar com a Apple | Doorgaan met Apple |
| Continue with Google | Continuar con Google | Continuer avec Google | Weiter mit Google | Continua con Google | Continuar com o Google | Doorgaan met Google |
| System Settings | Ajustes del Sistema | Réglages Système | Systemeinstellungen | Impostazioni di Sistema | Definições do Sistema | Systeeminstellingen |
| Privacy & Security | Privacidad y seguridad | Confidentialité et sécurité | Datenschutz & Sicherheit | Privacy e sicurezza | Privacidade e segurança | Privacy en beveiliging |
| Control Centre | Centro de control | Centre de contrôle | Kontrollzentrum | Centro di Controllo | Central de controlo | Bedieningspaneel |
| Login Items | Ítems de inicio | Éléments d’ouverture | Anmeldeobjekte | Elementi login | Elementos de início de sessão | Inlogonderdelen |
| Trash | Papelera | Corbeille | Papierkorb | Cestino | Lixo | Prullenmand |
| Downloads | Descargas | Téléchargements | Downloads | Download | Transferências | Downloads |
| Photos | Fotos | Photos | Fotos | Foto | Fotografias | Foto’s |
| menu bar | barra de menús | barre des menus | Menüleiste | barra dei menu | barra de menus | menubalk |

## Changing the glossary

- Change a term in `<code>.json`, or a name in `never.json`, then update the tables in this file
  (they are copies).
- Keep `en` exactly as the English writes it. Mark a term `enforce` only when every correct
  translation will contain `text` (see "How the check uses these files").
- Spanish changes need the owner's approval, since the owner reviews Spanish. The other languages
  need a native reviewer when one is available.
- A changed enforced term makes existing translations warn. Fix them in the same change.
