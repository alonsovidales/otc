// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.i18n

import android.content.Context
import android.content.res.Resources
import android.icu.number.NumberFormatter
import android.icu.text.NumberFormat
import android.os.Build
import androidx.annotation.PluralsRes
import androidx.annotation.StringRes
import androidx.compose.runtime.Composable
import androidx.compose.runtime.ReadOnlyComposable
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import java.util.Locale

// Text for the screen, kept as a resource id and its arguments until it is
// shown (docs/i18n.md, "Each platform"): view models hold UiText, never a
// resolved String, so whatever is on screen re-renders in a new language.
// The generated accessors build them - S.commonCancel(),
// S.appPhotosDeletedBy(name, count), from S_<Prefix>.kt - and the text comes
// from the resources make i18n writes (res/values*/strings_<prefix>.xml),
// where every argument is a positional %N$s. Composables call resolve();
// code outside composition passes a Context whose configuration carries the
// app's language. Numbers are formatted here with ICU for that
// configuration's locale and inserted as text; a <plurals> count is also
// the quantity, so the configuration's locale picks the plural form too -
// which is why the runtime always hands Android a language's full tag
// (Languages.kt: "pt-PT", never "pt").
//
// Translated text never goes through an HTML parser (no Html.fromHtml,
// HtmlCompat or AnnotatedString.fromHtml): a key with tags is a RichText,
// whose own parser knows only <name>...</name>.

/** The namespace of the generated accessors: S_<Prefix>.kt extend it. */
object S

/** Text for the screen, resolved in the app's language when it is shown. */
sealed interface UiText {
    /** A `<string>` resource; [args] fill its `%1$s`, `%2$s`... in order. */
    data class Res(@StringRes val id: Int, val args: List<Any> = emptyList()) : UiText

    /**
     * A `<plurals>` resource: [count] picks the form, and appears in [args]
     * too where the text shows it.
     */
    data class Plural(@PluralsRes val id: Int, val count: Int, val args: List<Any> = emptyList()) : UiText

    /** Text that isn't ours to translate: a detail from a device, a file name. */
    data class Raw(val text: String) : UiText

    /** The text in the language of the composition's resources. */
    @Composable
    @ReadOnlyComposable
    fun resolve(): String = resolve(LocalResources.current)

    /** The text in the language of [context]'s configuration. */
    fun resolve(context: Context): String = resolve(context.resources)

    /** The text in the language of [resources]' configuration. */
    fun resolve(resources: Resources): String = when (this) {
        is Raw -> text
        // A key without arguments is never formatted: its resource keeps a
        // literal % as it is (formatted="false").
        is Res ->
            if (args.isEmpty()) resources.getString(id)
            else resources.getString(id, *UiTextFormat.args(args, resources))
        is Plural ->
            if (args.isEmpty()) resources.getQuantityString(id, count)
            else resources.getQuantityString(id, count, *UiTextFormat.args(args, resources))
    }
}

/**
 * What a tag of a rich key does to its span: a style, a link (whose address
 * always comes from code), both, or - absent from the map - nothing.
 */
data class RichTag(val style: SpanStyle? = null, val link: LinkAnnotation? = null)

/**
 * The text of a key with tags ("Read the <link>privacy policy</link>"),
 * resolved to an [AnnotatedString]. The generated accessors of rich keys
 * return it instead of a [UiText], so a rich key can't be shown as a plain
 * String by mistake.
 */
sealed interface RichText {
    data class Res(@StringRes val id: Int, val args: List<Any> = emptyList()) : RichText

    data class Plural(@PluralsRes val id: Int, val count: Int, val args: List<Any> = emptyList()) : RichText

    /** The text in the language of the composition's resources, [tags] by name. */
    @Composable
    @ReadOnlyComposable
    fun resolve(tags: Map<String, RichTag>): AnnotatedString = resolve(LocalResources.current, tags)

    /** The text in the language of [context]'s configuration. */
    fun resolve(context: Context, tags: Map<String, RichTag>): AnnotatedString =
        resolve(context.resources, tags)

    /** The text in the language of [resources]' configuration. */
    fun resolve(resources: Resources, tags: Map<String, RichTag>): AnnotatedString {
        // Without arguments getString and getQuantityString return the
        // template itself, unformatted.
        val (template, args) = when (this) {
            is Res -> resources.getString(id) to args
            is Plural -> resources.getQuantityString(id, count) to args
        }
        val locale = UiTextFormat.locale(resources)
        val values = UiTextFormat.values(args, locale, UiTextFormat::icuInteger) { it.resolve(resources) }
        return UiTextFormat.rich(template, values, locale, tags)
    }
}

/** The formatting behind [UiText] and [RichText], apart from Resources so that JVM tests reach it. */
internal object UiTextFormat {
    /** The first private-use character: argument i's sentinel is SENTINEL + i. */
    const val SENTINEL = '\uE000'

    private val tagName = Regex("[a-z][a-z0-9]*")

    /** The locale of [resources]' configuration: its language, and how its numbers read. */
    fun locale(resources: Resources): Locale = resources.configuration.locales[0]

    fun args(args: List<Any>, resources: Resources): Array<Any> =
        values(args, locale(resources), ::icuInteger) { it.resolve(resources) }.toTypedArray()

    /**
     * Each argument as the text to insert: a number formatted by [number]
     * for [locale], a nested [UiText] resolved by [nested], a String as it is.
     */
    fun values(args: List<Any>, locale: Locale, number: (Long, Locale) -> String, nested: (UiText) -> String): List<String> =
        args.map { a ->
            when (a) {
                is String -> a
                is Int -> number(a.toLong(), locale)
                is Long -> number(a, locale)
                is Short -> number(a.toLong(), locale)
                is Byte -> number(a.toLong(), locale)
                is UiText -> nested(a)
                else -> a.toString()
            }
        }

    /**
     * An integer as [locale] writes it: "1,234" in English, "1 234" in French.
     * From API 30 ICU's NumberFormatter applies CLDR's minimum grouping, so
     * Spanish writes "1234" (and "12.345") like the web app's Intl and the
     * Apple apps; API 29's NumberFormat always groups ("1.234").
     */
    fun icuInteger(n: Long, locale: Locale): String =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) NumberFormatter.withLocale(locale).format(n).toString()
        else NumberFormat.getIntegerInstance(locale).format(n)

    /**
     * Builds a rich text from its [template] (the resource, tags as plain
     * `<name>` text) and its arguments' [values].
     *
     * The template is formatted with a sentinel in place of each argument
     * (a private-use character, which Check refuses in catalog texts), the
     * tags are parsed out of that, and only then is each sentinel swapped
     * for its value, as plain text: an argument is never scanned for tags,
     * so a file named "<b>.jpg" stays a file name.
     */
    fun rich(template: String, values: List<String>, locale: Locale, tags: Map<String, RichTag>): AnnotatedString {
        val text =
            if (values.isEmpty()) template
            else String.format(locale, template, *Array<Any>(values.size) { (SENTINEL + it).toString() })
        return buildAnnotatedString {
            val literal = StringBuilder()
            fun flush() {
                for (c in literal) {
                    val i = c - SENTINEL
                    if (i in values.indices) append(values[i]) else append(c)
                }
                literal.setLength(0)
            }
            var open: String? = null
            var mark = -1 // the builder's stack index of the open tag's first push
            var at = 0
            while (at < text.length) {
                val c = text[at]
                val end = if (c == '<') text.indexOf('>', at + 1) else -1
                if (end > 0) {
                    val inner = text.substring(at + 1, end)
                    val name = inner.removePrefix("/")
                    val closing = inner.length != name.length
                    val ok = tagName.matches(name) && if (closing) open == name else open == null
                    if (ok) {
                        flush()
                        if (closing) {
                            if (mark >= 0) pop(mark)
                            open = null
                            mark = -1
                        } else {
                            open = name
                            val tag = tags[name]
                            val styled = tag?.style?.let { pushStyle(it) } ?: -1
                            val linked = tag?.link?.let { pushLink(it) } ?: -1
                            mark = if (styled >= 0) styled else linked
                        }
                        at = end + 1
                        continue
                    }
                }
                literal.append(c)
                at++
            }
            flush()
        }
    }
}
