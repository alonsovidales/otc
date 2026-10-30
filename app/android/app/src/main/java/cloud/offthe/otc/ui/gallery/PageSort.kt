// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import android.content.Context
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.Sort
import androidx.compose.material.icons.filled.Check
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.PlainTooltip
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TooltipBox
import androidx.compose.material3.TooltipDefaults
import androidx.compose.material3.rememberTooltipState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appPhotosCollectionsSortName
import cloud.offthe.otc.i18n.appPhotosCollectionsSortNewest
import cloud.offthe.otc.i18n.appPhotosPeopleSortName
import cloud.offthe.otc.i18n.appPhotosPeopleSortPhotos
import cloud.offthe.otc.i18n.appPhotosSortBy
import cloud.offthe.otc.proto.ImageGroup
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.ui.files.NameCollator
import java.util.Locale

// Sort by on the People and Collections pages (owner's request 2026-10-30;
// iOS's PeopleSort and CollectionsSort, the same rules - change them
// together; tests PageSortTest.kt):
// - People: Most photos (the default: the page's order as it always was,
//   named people first, then the unnamed, each by photos) or Name: A to Z
//   in the app language's collation at primary strength (case and accents
//   ignored) with the numbers in a name by value (Files' NameCollator),
//   people without a name last, in the Most photos order;
// - Collections: Newest first (the default: the device's order, the
//   collections made most recently first) or Name, A to Z the same way,
//   collections without a name ("Untitled") last, in the device's order;
// - two with the same name keep the default order between them;
// - one choice per page for the app, kept in otc_settings as Files' view
//   mode is (people_sort, collections_sort): backed up, wiped at Log Out.

/** People's orders, as they are kept (people_sort). */
enum class PeopleSort(val stored: String) {
    PHOTOS("photos"), NAME("name");

    val label: UiText get() = if (this == NAME) S.appPhotosPeopleSortName() else S.appPhotosPeopleSortPhotos()

    companion object {
        const val STORAGE_KEY = "people_sort"
        val DEFAULT = PHOTOS

        /** A stored order, or the default for anything else (nothing stored, a newer build's). */
        fun parse(stored: String?): PeopleSort = entries.firstOrNull { it.stored == stored } ?: DEFAULT
    }
}

/** Collections' orders, as they are kept (collections_sort). */
enum class CollectionsSort(val stored: String) {
    NEWEST("newest"), NAME("name");

    val label: UiText get() = if (this == NAME) S.appPhotosCollectionsSortName() else S.appPhotosCollectionsSortNewest()

    companion object {
        const val STORAGE_KEY = "collections_sort"
        val DEFAULT = NEWEST

        fun parse(stored: String?): CollectionsSort = entries.firstOrNull { it.stored == stored } ?: DEFAULT
    }
}

object PageSort {
    /** The People page's faces ([shown]: in its default order, peopleOrder) in [sort]. */
    fun people(
        shown: List<Person>, sort: PeopleSort, locale: Locale,
        collator: (Locale) -> NameCollator = NameCollator::icu,
    ): List<Person> = if (sort == PeopleSort.NAME) byName(shown, { it.name }, locale, collator) else shown

    /** The collections (as the device lists them: newest first) in [sort]. */
    fun collections(
        groups: List<ImageGroup>, sort: CollectionsSort, locale: Locale,
        collator: (Locale) -> NameCollator = NameCollator::icu,
    ): List<ImageGroup> = if (sort == CollectionsSort.NAME) byName(groups, { it.name }, locale, collator) else groups

    /**
     * [items] with a name A to Z, then those without one ([name] blank) in
     * the order given; the same name keeps the order given too (a stable
     * sort on collation keys worked out once each).
     */
    internal fun <T> byName(items: List<T>, name: (T) -> String, locale: Locale, collator: (Locale) -> NameCollator): List<T> {
        val (named, unnamed) = items.partition { name(it).isNotBlank() }
        if (named.size < 2) return named + unnamed
        val keys = collator(locale)
        val sorted = named.map { it to keys.key(name(it).replace(Regex("\\s+"), " ").trim()) }
            .sortedWith { a, b -> compareKeys(a.second, b.second) }
            .map { it.first }
        return sorted + unnamed
    }

    /** Collation keys, unsigned byte by byte (as ICU's CollationKey.compareTo). */
    private fun compareKeys(a: ByteArray, b: ByteArray): Int {
        val n = minOf(a.size, b.size)
        for (i in 0 until n) {
            val c = (a[i].toInt() and 0xff) - (b[i].toInt() and 0xff)
            if (c != 0) return c
        }
        return a.size - b.size
    }
}

/**
 * Where the two choices are kept: otc_settings, beside files_view_mode
 * (backed up with it, wiped at Log Out by SecretsStore.logOut). Read when a
 * page shows, so a Log Out's wipe is the default again.
 */
internal object PageSortStore {
    private fun prefs() = OTCApp.instance.getSharedPreferences("otc_settings", Context.MODE_PRIVATE)
    private fun read(key: String): String? = try { prefs().getString(key, null) } catch (_: Exception) { null }
    private fun write(key: String, value: String) { try { prefs().edit().putString(key, value).apply() } catch (_: Exception) {} }

    fun people(): PeopleSort = PeopleSort.parse(read(PeopleSort.STORAGE_KEY))
    fun setPeople(s: PeopleSort) = write(PeopleSort.STORAGE_KEY, s.stored)
    fun collections(): CollectionsSort = CollectionsSort.parse(read(CollectionsSort.STORAGE_KEY))
    fun setCollections(s: CollectionsSort) = write(CollectionsSort.STORAGE_KEY, s.stored)
}

/**
 * The Sort by button and its menu: "Sort by" as the menu's heading, each
 * order with the check on the one in use. [labelled]: the sort icon and
 * the order in use in words (a wide page, with room for them), else the
 * icon alone with its tooltip (a bar). TalkBack hears "Sort by" and the
 * order in use either way; 48 dp to touch.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun <T> SortByMenu(
    options: List<T>, current: T, label: (T) -> UiText, onPick: (T) -> Unit,
    modifier: Modifier = Modifier, labelled: Boolean = false,
) {
    var open by remember { mutableStateOf(false) }
    val title = S.appPhotosSortBy().resolve()
    val now = label(current).resolve()
    val colors = MaterialTheme.colorScheme
    Box(modifier) {
        if (labelled) {
            TextButton(
                onClick = { open = true }, contentPadding = PaddingValues(start = 12.dp, end = 16.dp),
                modifier = Modifier.heightIn(min = 48.dp).semantics { contentDescription = title; stateDescription = now },
            ) {
                Icon(Icons.AutoMirrored.Filled.Sort, null, Modifier.size(20.dp))
                Spacer(Modifier.width(8.dp))
                Text(now, fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
        } else {
            TooltipBox(positionProvider = TooltipDefaults.rememberPlainTooltipPositionProvider(), tooltip = { PlainTooltip { Text(title) } }, state = rememberTooltipState()) {
                IconButton(onClick = { open = true }, modifier = Modifier.semantics { stateDescription = now }) {
                    Icon(Icons.AutoMirrored.Filled.Sort, title)
                }
            }
        }
        DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            Text(
                title, style = MaterialTheme.typography.labelMedium, color = colors.onSurfaceVariant,
                modifier = Modifier.padding(horizontal = 12.dp, vertical = 8.dp).semantics { heading() },
            )
            for (o in options) {
                val on = o == current
                DropdownMenuItem(
                    text = { Text(label(o).resolve()) },
                    onClick = { open = false; if (!on) onPick(o) },
                    leadingIcon = { if (on) Icon(Icons.Default.Check, null) else Spacer(Modifier.size(24.dp)) },
                    modifier = Modifier.semantics { selected = on },
                )
            }
        }
    }
}
