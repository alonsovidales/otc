// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.i18n

import cloud.offthe.otc.R
import org.w3c.dom.Element
import java.io.File
import java.text.NumberFormat
import java.util.Locale
import javax.xml.parsers.DocumentBuilderFactory

// What a UiText says in English, for JVM tests: there are no Android
// resources here, so the generated res/values/strings_*.xml are read as
// files and formatted as Android would - each resource's name found from
// its id in R, the escapes aapt2 reads undone, numbers in English, the
// plural form by English rules (one for 1). A test comparing a view
// model's text with the English it showed before the catalog keeps its
// expected string: only `.english()` is added on the other side.

/** This text in English, as the app shows it in English. */
fun UiText.english(): String = when (this) {
    is UiText.Raw -> text
    is UiText.Res -> EnglishStrings.format(EnglishStrings.string(id), args)
    is UiText.Plural -> EnglishStrings.format(EnglishStrings.plural(id, count), args)
}

internal object EnglishStrings {
    private class Entry(val text: String, val formatted: Boolean)

    private val strings = mutableMapOf<String, Entry>()
    private val plurals = mutableMapOf<String, Map<String, Entry>>()

    init {
        val dir = listOf(File("src/main/res/values"), File("app/src/main/res/values")).first { it.isDirectory }
        val files = dir.listFiles { f -> f.name.startsWith("strings") && f.name.endsWith(".xml") }.orEmpty()
        for (f in files.sortedBy { it.name }) {
            val doc = DocumentBuilderFactory.newInstance().newDocumentBuilder().parse(f)
            val root = doc.documentElement
            val kids = root.childNodes
            for (i in 0 until kids.length) {
                val e = kids.item(i) as? Element ?: continue
                val name = e.getAttribute("name")
                val formatted = e.getAttribute("formatted") != "false"
                when (e.tagName) {
                    "string" -> strings[name] = Entry(unescape(e.textContent), formatted)
                    "plurals" -> {
                        val forms = mutableMapOf<String, Entry>()
                        val items = e.getElementsByTagName("item")
                        for (j in 0 until items.length) {
                            val item = items.item(j) as Element
                            forms[item.getAttribute("quantity")] = Entry(unescape(item.textContent), formatted)
                        }
                        plurals[name] = forms
                    }
                }
            }
        }
    }

    private fun nameOf(id: Int, of: Class<*>): String =
        of.fields.firstOrNull { it.type == Int::class.javaPrimitiveType && it.getInt(null) == id }?.name
            ?: error("no resource with id $id in ${of.simpleName}")

    fun string(id: Int): Pair<String, Boolean> {
        val e = strings[nameOf(id, R.string::class.java)] ?: error("no English string for id $id")
        return e.text to e.formatted
    }

    fun plural(id: Int, count: Int): Pair<String, Boolean> {
        val name = nameOf(id, R.plurals::class.java)
        val forms = plurals[name] ?: error("no English plurals $name")
        val e = forms[if (count == 1) "one" else "other"] ?: forms.getValue("other")
        return e.text to e.formatted
    }

    fun format(template: Pair<String, Boolean>, args: List<Any>): String {
        val (text, formatted) = template
        if (args.isEmpty() || !formatted) return text
        val values = args.map { a ->
            when (a) {
                is String -> a
                is Int, is Long, is Short, is Byte -> NumberFormat.getIntegerInstance(Locale.US).format((a as Number).toLong())
                is UiText -> a.english()
                else -> a.toString()
            }
        }
        return String.format(Locale.US, text, *values.toTypedArray())
    }

    /** aapt2's reading of a string resource's escapes. */
    private fun unescape(s: String): String {
        val b = StringBuilder()
        var i = 0
        while (i < s.length) {
            val c = s[i]
            if (c == '\\' && i + 1 < s.length) {
                val n = s[i + 1]
                when (n) {
                    'n' -> b.append('\n')
                    't' -> b.append('\t')
                    'u' -> {
                        b.append(s.substring(i + 2, i + 6).toInt(16).toChar())
                        i += 6
                        continue
                    }
                    else -> b.append(n) // \' \" \\ \@ \?
                }
                i += 2
                continue
            }
            b.append(c)
            i++
        }
        return b.toString()
    }
}
