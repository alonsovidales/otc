// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.proto.ImageGroup
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.proto.PlaceArea
import cloud.offthe.otc.ui.files.NameCollator
import com.ibm.icu.text.Collator
import com.ibm.icu.text.RuleBasedCollator
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.Locale

// Sort by on People and Collections (PageSort.kt), and when Images shows
// "Back to the map" (showsAreaBack). iOS's PeopleSort / CollectionsSort /
// AreaBackButton follow the same rules: change them together.
class PageSortTest {
    private val en: Locale = Locale.ENGLISH
    private val es: Locale = Locale.forLanguageTag("es")

    // The phone's collator is android.icu's (NameCollator.icu); JVM tests
    // have only android.jar's stubs of it, so ICU4J stands in, set up the
    // same way (as FilesSortTest's).
    private val icu4j: (Locale) -> NameCollator = { locale ->
        val c = Collator.getInstance(locale)
        c.strength = Collator.PRIMARY
        c.decomposition = Collator.CANONICAL_DECOMPOSITION
        (c as RuleBasedCollator).numericCollation = true
        NameCollator { c.getCollationKey(it).toByteArray() }
    }

    private fun p(id: String, name: String, n: Int) = Person.newBuilder().setId(id).setName(name).setFaceCount(n).build()
    private fun g(id: String, name: String) = ImageGroup.newBuilder().setId(id).setName(name).build()

    private fun people(all: List<Person>, sort: PeopleSort, locale: Locale = en) =
        PageSort.people(peopleOrder(all, emptySet()), sort, locale, icu4j).map { it.id }

    private fun collections(all: List<ImageGroup>, sort: CollectionsSort, locale: Locale = en) =
        PageSort.collections(all, sort, locale, icu4j).map { it.id }

    // ---- People ----

    private val faces = listOf(
        p("u1", "", 80), p("bea", "Bea", 40), p("alv", "Álvaro", 12), p("u2", "  ", 30),
        p("ana", "ana", 9), p("zoe", "Zoe", 50), p("bru", "bruno", 3), p("u3", "", 5),
    )

    @Test fun mostPhotosIsThePagesOrderAsItAlwaysWas() {
        // Named first, then the unnamed, each by photos (peopleOrder).
        assertEquals(listOf("zoe", "bea", "alv", "ana", "bru", "u1", "u2", "u3"), people(faces, PeopleSort.PHOTOS))
    }

    @Test fun nameIsAToZIgnoringCaseAndAccentsWithTheUnnamedLastInTheirOrder() {
        assertEquals(listOf("alv", "ana", "bea", "bru", "zoe", "u1", "u2", "u3"), people(faces, PeopleSort.NAME))
    }

    @Test fun numbersInANameGoByTheirValue() {
        val all = listOf(p("p10", "Person 10", 9), p("p2", "Person 2", 8), p("p1", "Person 1", 7))
        assertEquals(listOf("p1", "p2", "p10"), people(all, PeopleSort.NAME))
    }

    @Test fun theSameNameKeepsMostPhotosFirst() {
        val all = listOf(p("a1", "Ana", 3), p("leo", "Leo", 9), p("a2", "ANA ", 20), p("a3", "Ána", 7))
        assertEquals(listOf("a2", "a3", "a1", "leo"), people(all, PeopleSort.NAME))
    }

    @Test fun spacesInANameDontMoveIt() {
        val all = listOf(p("b", "  Bea", 1), p("a", "Ana   Maria", 1), p("c", "Ana Lu", 1))
        assertEquals(listOf("c", "a", "b"), people(all, PeopleSort.NAME))
    }

    @Test fun theAppsLanguageDecides() {
        // Spanish has ñ as a letter of its own, after n; English sorts it as n.
        val all = listOf(p("o", "Óscar", 1), p("nu", "Nuria", 1), p("na", "Ñaki", 1))
        assertEquals(listOf("nu", "na", "o"), people(all, PeopleSort.NAME, es))
        assertEquals(listOf("na", "nu", "o"), people(all, PeopleSort.NAME, en))
    }

    @Test fun nothingOrOneToSortStaysAsItIs() {
        assertEquals(emptyList<String>(), people(emptyList(), PeopleSort.NAME))
        assertEquals(listOf("a", "u"), people(listOf(p("u", "", 9), p("a", "Ana", 1)), PeopleSort.NAME))
    }

    @Test fun peopleSortAsKept() {
        assertEquals("people_sort", PeopleSort.STORAGE_KEY)
        assertEquals(PeopleSort.PHOTOS, PeopleSort.DEFAULT)
        assertEquals(PeopleSort.PHOTOS, PeopleSort.parse(null))
        assertEquals(PeopleSort.PHOTOS, PeopleSort.parse("photos"))
        assertEquals(PeopleSort.NAME, PeopleSort.parse("name"))
        // A newer build's order, or anything else: the default.
        assertEquals(PeopleSort.PHOTOS, PeopleSort.parse("recent"))
        assertEquals(PeopleSort.PHOTOS, PeopleSort.parse("Name"))
        for (s in PeopleSort.entries) assertEquals(s, PeopleSort.parse(s.stored))
    }

    // ---- Collections ----

    // As the device lists them: newest first.
    private val groups = listOf(
        g("trip10", "Trip 10"), g("x1", ""), g("zoo", "zoo"), g("trip2", "Trip 2"),
        g("easter", "Éaster"), g("x2", "   "), g("bday", "Birthday"), g("bday2", "birthday"),
    )

    @Test fun newestFirstIsTheDevicesOrder() {
        assertEquals(groups.map { it.id }, collections(groups, CollectionsSort.NEWEST))
    }

    @Test fun collectionsByNameWithTheUntitledLast() {
        assertEquals(
            listOf("bday", "bday2", "easter", "trip2", "trip10", "zoo", "x1", "x2"),
            collections(groups, CollectionsSort.NAME),
        )
    }

    @Test fun collectionsSortAsKept() {
        assertEquals("collections_sort", CollectionsSort.STORAGE_KEY)
        assertEquals(CollectionsSort.NEWEST, CollectionsSort.DEFAULT)
        assertEquals(CollectionsSort.NEWEST, CollectionsSort.parse(null))
        assertEquals(CollectionsSort.NAME, CollectionsSort.parse("name"))
        assertEquals(CollectionsSort.NEWEST, CollectionsSort.parse("oldest"))
        for (s in CollectionsSort.entries) assertEquals(s, CollectionsSort.parse(s.stored))
    }

    // ---- Back to the map ----

    private val area = PlaceArea.newBuilder().setSouth(43.0).setWest(-5.9).setNorth(43.1).setEast(-5.8).build()

    @Test fun backToTheMapOverAPlacesPhotos() {
        assertTrue(showsAreaBack(area, placesOn = true, mapShown = false))
    }

    @Test fun noBackToTheMapWithoutAPlace() {
        assertFalse(showsAreaBack(null, placesOn = true, mapShown = false))
    }

    @Test fun noBackToTheMapWhileTheMapShows() {
        assertFalse(showsAreaBack(area, placesOn = true, mapShown = true))
    }

    @Test fun noBackToTheMapOncePlacesIsOffOrNotKnown() {
        assertFalse(showsAreaBack(area, placesOn = false, mapShown = false))
        assertFalse(showsAreaBack(area, placesOn = null, mapShown = false))
    }
}
