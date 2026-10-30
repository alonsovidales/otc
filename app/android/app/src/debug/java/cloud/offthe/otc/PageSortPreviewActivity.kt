// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.systemBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import cloud.offthe.otc.i18n.LanguageSettings
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appShellCollectionUntitled
import cloud.offthe.otc.i18n.appShellSearchUnnamed
import cloud.offthe.otc.i18n.appShellSectionPeople
import cloud.offthe.otc.proto.ImageGroup
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.ui.gallery.AreaBackButton
import cloud.offthe.otc.ui.gallery.CollectionsSort
import cloud.offthe.otc.ui.gallery.PageSort
import cloud.offthe.otc.ui.gallery.PeopleSort
import cloud.offthe.otc.ui.gallery.SortByMenu
import cloud.offthe.otc.ui.gallery.gridMetrics
import cloud.offthe.otc.ui.gallery.peopleOrder
import cloud.offthe.otc.ui.theme.OTCTheme

/**
 * Debug builds only: People's and Collections' Sort by and Images' "Back to
 * the map" on sample data, no device needed (adb shell am start -n
 * cloud.offthe.otc/.PageSortPreviewActivity). People's bar as on a narrow
 * window (the icon), Collections' toolbar as on the wide page (the order in
 * words); each list below follows its menu, as the pages do. Nothing is
 * kept: the pages' choice in otc_settings is left alone.
 */
class PageSortPreviewActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        setContent {
            OTCTheme {
                Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
                    val colors = MaterialTheme.colorScheme
                    val locale = LanguageSettings.numberLocale
                    var peopleSort by remember { mutableStateOf(PeopleSort.DEFAULT) }
                    var collectionsSort by remember { mutableStateOf(CollectionsSort.DEFAULT) }
                    val people = remember(peopleSort, locale) { PageSort.people(peopleOrder(samplePeople, emptySet()), peopleSort, locale) }
                    val groups = remember(collectionsSort, locale) { PageSort.collections(sampleGroups, collectionsSort, locale) }
                    Column(Modifier.fillMaxSize().systemBarsPadding().verticalScroll(rememberScrollState())) {
                        // Images over a place opened from the map.
                        AreaBackButton(gridMetrics(wide = false, windowDp = 400)) {}
                        Spacer(Modifier.height(16.dp))
                        HorizontalDivider()
                        // People's bar, narrow.
                        Row(
                            Modifier.fillMaxWidth().height(64.dp).background(colors.surfaceContainerLow).padding(start = 4.dp, end = 4.dp),
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            IconButton(onClick = {}) { Icon(Icons.AutoMirrored.Filled.ArrowBack, null) }
                            Spacer(Modifier.width(4.dp))
                            Text(S.appShellSectionPeople().resolve(), Modifier.weight(1f), style = MaterialTheme.typography.titleLarge)
                            SortByMenu(PeopleSort.entries, peopleSort, { it.label }, onPick = { peopleSort = it })
                        }
                        for (p in people) Text(
                            p.name.trim().ifEmpty { S.appShellSearchUnnamed().resolve() } + " · ${p.faceCount}",
                            Modifier.padding(horizontal = 20.dp, vertical = 4.dp),
                            color = if (p.name.isBlank()) colors.onSurfaceVariant else colors.onSurface,
                        )
                        Spacer(Modifier.height(16.dp))
                        HorizontalDivider()
                        // Collections' toolbar, wide.
                        Row(Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, top = 8.dp), horizontalArrangement = Arrangement.End) {
                            SortByMenu(CollectionsSort.entries, collectionsSort, { it.label }, onPick = { collectionsSort = it }, labelled = true)
                        }
                        for (g in groups) Text(
                            g.name.trim().ifEmpty { S.appShellCollectionUntitled().resolve() },
                            Modifier.padding(horizontal = 20.dp, vertical = 4.dp),
                        )
                        Spacer(Modifier.height(24.dp))
                    }
                }
            }
        }
    }

    private companion object {
        fun person(id: String, name: String, n: Int): Person = Person.newBuilder().setId(id).setName(name).setFaceCount(n).build()
        fun group(id: String, name: String): ImageGroup = ImageGroup.newBuilder().setId(id).setName(name).build()

        // As ListPeople answers: most photos first.
        val samplePeople = listOf(
            person("1", "Zoe", 412), person("2", "", 380), person("3", "Álvaro", 257), person("4", "bea", 198),
            person("5", "Person 10", 120), person("6", "Ana", 96), person("7", "", 40), person("8", "Person 2", 12),
        )

        // As ListImageGroups answers: newest first.
        val sampleGroups = listOf(
            group("a", "Trip 10"), group("b", "Birthday"), group("c", ""), group("d", "Éaster"),
            group("e", "Trip 2"), group("f", "zoo"),
        )
    }
}
