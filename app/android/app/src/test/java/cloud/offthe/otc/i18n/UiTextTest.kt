// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.i18n

import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.font.FontWeight
import cloud.offthe.otc.R
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.text.NumberFormat
import java.util.Locale

// UiText on the JVM: what the accessors build, and the formatting behind
// resolve() apart from Resources (android.icu and Resources are stubs here,
// so numbers go through java.text in these tests).
class UiTextTest {
    private val number = { n: Long, l: Locale -> "#" + NumberFormat.getIntegerInstance(l).format(n) }
    private val nested = { t: UiText -> "nested:$t" }

    @Test
    fun accessorsBuildComparableValues() {
        // View models hold these, so tests compare them as values.
        assertEquals(UiText.Res(R.string.common_cancel), S.commonCancel())
        assertEquals(S.commonCancel(), S.commonCancel())
        assertNotEquals(S.commonCancel(), S.commonDelete())
        assertEquals(UiText.Raw("x"), UiText.Raw("x"))
        assertEquals(UiText.Plural(1, 2, listOf("a", 2)), UiText.Plural(1, 2, listOf("a", 2)))
        assertNotEquals(UiText.Plural(1, 2, listOf("a", 2)), UiText.Plural(1, 3, listOf("a", 3)))
    }

    @Test
    fun languagesStartWithEnglish() {
        assertEquals(Languages.SOURCE, Languages.all.first().code)
        assertEquals("en", Languages.forCode("en")?.tag)
        assertEquals(null, Languages.forCode("xx"))
    }

    @Test
    fun argumentsBecomeText() {
        val v = UiTextFormat.values(listOf("Ana", 1234, 5L, UiText.Raw("r"), 'c'), Locale.ENGLISH, number, nested)
        assertEquals(listOf("Ana", "#1,234", "#5", "nested:Raw(text=r)", "c"), v)
        // A number is formatted for the configuration's locale.
        val de = UiTextFormat.values(listOf(1234567), Locale.GERMAN, number, nested)
        assertEquals(listOf("#1.234.567"), de)
    }

    @Test
    fun richTextParsesTagsBeforeInsertingArguments() {
        val link = LinkAnnotation.Url("https://example.org/privacy")
        val bold = SpanStyle(fontWeight = FontWeight.Bold)
        val t = UiTextFormat.rich(
            "Read the <link>privacy policy</link> of %1\$s: <b>%2\$s</b> left, 100%% sure",
            listOf("<b>Ana</b> \uE001", "5"),
            Locale.ENGLISH,
            mapOf("link" to RichTag(link = link), "b" to RichTag(style = bold)),
        )
        // The argument's own "<b>" and the sentinel-looking character stay
        // literal: arguments are inserted after the tags are parsed.
        assertEquals("Read the privacy policy of <b>Ana</b> \uE001: 5 left, 100% sure", t.text)
        val links = t.getLinkAnnotations(0, t.length)
        assertEquals(1, links.size)
        assertEquals(link, links[0].item)
        assertEquals("privacy policy", t.text.substring(links[0].start, links[0].end))
        assertEquals(1, t.spanStyles.size)
        assertEquals(bold, t.spanStyles[0].item)
        assertEquals("5", t.text.substring(t.spanStyles[0].start, t.spanStyles[0].end))
    }

    @Test
    fun richTextWithoutArgumentsIsNotFormatted() {
        // A key without arguments keeps a literal % (formatted="false").
        val t = UiTextFormat.rich("<b>50%</b> off %s", emptyList(), Locale.ENGLISH, emptyMap())
        assertEquals("50% off %s", t.text)
        assertTrue(t.spanStyles.isEmpty())
    }

    @Test
    fun richTextLeavesAnythingElseAsText() {
        val bold = SpanStyle(fontWeight = FontWeight.Bold)
        val tags = mapOf("b" to RichTag(style = bold), "i" to RichTag(style = bold))
        // Unknown tags render their text plain; a stray ">", a "<" that opens
        // nothing, a close without an open and a nested tag stay text.
        val t = UiTextFormat.rich("a > b <x>c</x> <b>d <i>e</i></b> </i> < f <B>g</B>", emptyList(), Locale.ENGLISH, tags)
        assertEquals("a > b c d <i>e</i> </i> < f <B>g</B>", t.text)
        assertEquals(1, t.spanStyles.size)
        assertEquals("d <i>e</i>", t.text.substring(t.spanStyles[0].start, t.spanStyles[0].end))
        // A tag left open runs to the end.
        val open = UiTextFormat.rich("x <b>y", emptyList(), Locale.ENGLISH, tags)
        assertEquals("x y", open.text)
        assertEquals("y", open.text.substring(open.spanStyles[0].start, open.spanStyles[0].end))
    }

    @Test
    fun richTextArgumentsKeepTheirPositions() {
        // A translation may reorder the arguments; each sentinel finds its own.
        val t = UiTextFormat.rich("%2\$s, <b>%1\$s</b>", listOf("first", "second"), Locale.ENGLISH, emptyMap())
        assertEquals("second, first", t.text)
    }
}
