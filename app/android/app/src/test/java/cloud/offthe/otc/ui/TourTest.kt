// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.geometry.Size
import cloud.offthe.otc.i18n.EnglishStrings
import cloud.offthe.otc.i18n.RichText
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiTextFormat
import cloud.offthe.otc.i18n.appTourProgress
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.ui.TourStep.ALERTS
import cloud.offthe.otc.ui.TourStep.BACKUP
import cloud.offthe.otc.ui.TourStep.COLLECTIONS
import cloud.offthe.otc.ui.TourStep.DATES
import cloud.offthe.otc.ui.TourStep.FILES
import cloud.offthe.otc.ui.TourStep.FRIENDS
import cloud.offthe.otc.ui.TourStep.PEOPLE
import cloud.offthe.otc.ui.TourStep.POST
import cloud.offthe.otc.ui.TourStep.SEARCH
import cloud.offthe.otc.ui.TourStep.SELECT
import cloud.offthe.otc.ui.TourStep.SOCIAL
import cloud.offthe.otc.ui.TourStep.UPLOAD
import cloud.offthe.otc.ui.TourStep.USERS
import cloud.offthe.otc.ui.TourStep.VIEWER
import cloud.offthe.otc.ui.TourStep.WELCOME
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import java.io.File
import java.text.NumberFormat
import java.util.Locale

// The first-run tour's rules and model (Tour.kt): its steps, when it opens
// by itself, the mark kept on the phone, moving between steps while People
// and Users come and go, the section each step shows, what it rings, the
// holes and where the card goes. iOS's TourTests.swift has the same cases,
// under the same names (T1-T19).
class TourTest {
    private val all = listOf(WELCOME, SEARCH, DATES, SELECT, VIEWER, PEOPLE, COLLECTIONS, FILES, UPLOAD, SOCIAL, POST, FRIENDS, BACKUP, USERS, ALERTS)
    private val noFaces = all - PEOPLE
    private val noOwner = all - USERS
    private val fewest = all - PEOPLE - USERS

    /** A mark kept in memory: what was read and written. */
    private class FakeStore(var value: Int?) : TourStore {
        var writable = true
        val writes = mutableListOf<Int>()
        override fun seenVersion(): Int? = value
        override fun markSeen(version: Int): Boolean {
            writes += version
            if (!writable) return false
            value = version
            return true
        }
    }

    @Before fun fresh() = TourModel.reset()

    @After fun back() {
        TourModel.reset()
        TourModel.store = PrefsTourStore
        TourModel.role = { null }
    }

    private fun model(store: TourStore, owner: Boolean? = null): TourModel {
        TourModel.store = store
        TourModel.role = { owner }
        return TourModel
    }

    @Test fun t1TheVersionIsTwo() {
        assertEquals(2, TourRules.VERSION)
    }

    @Test fun t2PeopleWithFaceRecognitionAndUsersForTheOwnerOnly() {
        assertEquals(all, TourRules.steps(faces = true, owner = true))
        assertEquals(noFaces, TourRules.steps(faces = false, owner = true))
        assertEquals(noOwner, TourRules.steps(faces = true, owner = false))
        // Not known yet: left out, as the menu and Settings do.
        assertEquals(fewest, TourRules.steps(faces = null, owner = null))
        assertEquals(15, TourRules.steps(true, true).size)
        assertEquals(13, TourRules.steps(false, false).size)
    }

    @Test fun t3OpensByItselfOnlyWhenEverythingIsReady() {
        assertTrue(TourRules.shouldShow(seenVersion = 0, syncChoicePending = false, signedIn = true, mainVisible = true))
        // A phone that saw the first tour sees this one.
        assertTrue(TourRules.shouldShow(1, false, true, true))
        // Seen, this version or a later one.
        assertFalse(TourRules.shouldShow(2, false, true, true))
        assertFalse(TourRules.shouldShow(3, false, true, true))
        // The mark can't be read.
        assertFalse(TourRules.shouldShow(null, false, true, true))
        // The sync question first.
        assertFalse(TourRules.shouldShow(0, true, true, true))
        // Not signed in yet (or any more).
        assertFalse(TourRules.shouldShow(0, false, false, true))
        // The connection screen, the Bluetooth setup, a card over MainView.
        assertFalse(TourRules.shouldShow(0, false, true, false))
    }

    @Test fun t4NextAndBackFollowTheFixedOrder() {
        assertEquals(SEARCH, TourRules.after(WELCOME, all))
        assertEquals(PEOPLE, TourRules.after(VIEWER, all))
        assertEquals(COLLECTIONS, TourRules.after(VIEWER, noFaces))
        assertEquals(USERS, TourRules.after(BACKUP, all))
        assertEquals(ALERTS, TourRules.after(BACKUP, noOwner))
        assertNull(TourRules.after(ALERTS, all))
        assertNull(TourRules.before(WELCOME, all))
        assertEquals(WELCOME, TourRules.before(SEARCH, all))
        assertEquals(PEOPLE, TourRules.before(COLLECTIONS, all))
        assertEquals(VIEWER, TourRules.before(COLLECTIONS, noFaces))
        assertEquals(BACKUP, TourRules.before(ALERTS, fewest))
    }

    @Test fun t5AStepThatWentMovesToTheNextOne() {
        assertEquals(COLLECTIONS, TourRules.place(PEOPLE, noFaces))
        assertEquals(PEOPLE, TourRules.place(PEOPLE, all))
        assertEquals(ALERTS, TourRules.place(USERS, noOwner))
        assertEquals(FILES, TourRules.place(FILES, fewest))
        // From a step that went, Next and Back go on from where it was.
        assertEquals(COLLECTIONS, TourRules.after(PEOPLE, noFaces))
        assertEquals(VIEWER, TourRules.before(PEOPLE, noFaces))
    }

    @Test fun t6OpensOnceAndIsMarkedSeenAsItOpens() {
        val store = FakeStore(0)
        val tour = model(store)
        tour.evaluate(syncChoicePending = false, signedIn = true, mainVisible = true)
        assertTrue(tour.shown.value)
        assertEquals(WELCOME, tour.step.value)
        assertEquals(2, store.value)
        tour.close()
        // A relaunch, another sign-in: seen.
        tour.evaluate(false, true, true)
        assertFalse(tour.shown.value)
        assertEquals(listOf(2), store.writes)
        // A phone that saw the first tour sees this one, once.
        val older = FakeStore(1)
        model(older).evaluate(false, true, true)
        assertTrue(TourModel.shown.value)
        assertEquals(2, older.value)
    }

    @Test fun t7WaitsForTheSyncQuestionWithoutMarkingIt() {
        val store = FakeStore(0)
        val tour = model(store)
        tour.evaluate(syncChoicePending = true, signedIn = true, mainVisible = true)
        tour.evaluate(syncChoicePending = false, signedIn = false, mainVisible = true)
        tour.evaluate(syncChoicePending = false, signedIn = true, mainVisible = false)
        assertFalse(tour.shown.value)
        assertTrue(store.writes.isEmpty())
        tour.evaluate(false, true, true)
        assertTrue(tour.shown.value)
    }

    @Test fun t8AMarkThatCantBeReadOrKeptOpensNothing() {
        val unreadable = FakeStore(null)
        model(unreadable).evaluate(false, true, true)
        assertFalse(TourModel.shown.value)
        assertTrue(unreadable.writes.isEmpty())

        val unwritable = FakeStore(0).apply { writable = false }
        model(unwritable).evaluate(false, true, true)
        assertFalse(TourModel.shown.value)
        assertEquals(listOf(2), unwritable.writes)
    }

    @Test fun t9ReplayStartsAtTheFirstStepAndLeavesTheMark() {
        val store = FakeStore(2)
        val tour = model(store)
        tour.replay()
        assertTrue(tour.shown.value)
        assertEquals(WELCOME, tour.step.value)
        tour.next(all)
        tour.next(all)
        assertEquals(DATES, tour.step.value)
        tour.close()
        tour.replay()
        assertEquals(WELCOME, tour.step.value)
        assertTrue(store.writes.isEmpty())
        assertEquals(2, store.value)
    }

    @Test fun t10NextWalksToTheLastAndDoneCloses() {
        val tour = model(FakeStore(2))
        tour.replay()
        tour.back(fewest)
        assertEquals(WELCOME, tour.step.value)
        repeat(fewest.size - 1) { tour.next(fewest) }
        assertEquals(ALERTS, tour.step.value)
        assertTrue(tour.shown.value)
        tour.back(fewest)
        assertEquals(BACKUP, tour.step.value)
        tour.next(fewest)
        tour.next(fewest)
        assertFalse(tour.shown.value)
    }

    @Test fun t11PeopleGoingWhileShownMovesOn() {
        val tour = model(FakeStore(2))
        tour.replay()
        repeat(5) { tour.next(all) }
        assertEquals(PEOPLE, tour.step.value)
        // Face recognition turned off meanwhile: the step shown is the next.
        assertEquals(COLLECTIONS, TourRules.place(tour.step.value, noFaces))
        tour.next(noFaces)
        assertEquals(FILES, tour.step.value)
        tour.back(noFaces)
        tour.back(noFaces)
        assertEquals(VIEWER, tour.step.value)
    }

    @Test fun t12LogOutClosesItAndTheMarkStays() = runBlocking {
        val store = FakeStore(0)
        val tour = model(store, owner = true)
        tour.evaluate(false, true, true)
        tour.learnOwner()
        tour.next(all)
        tour.reset()
        assertFalse(tour.shown.value)
        assertEquals(WELCOME, tour.step.value)
        assertNull(tour.owner.value)
        // The next sign-in, on this device or another: not again.
        tour.evaluate(false, true, true)
        assertFalse(tour.shown.value)
        assertEquals(2, store.value)
    }

    @Test fun t13TheMarkIsKeptInPrefsOfItsOwn() {
        assertEquals("otc_tour", PrefsTourStore.PREFS)
        assertEquals("seen_version", PrefsTourStore.KEY)
        // Never backed up nor moved to another phone: the rules list only otc_settings.
        val dir = listOf(File("src/main/res/xml"), File("app/src/main/res/xml")).first { it.isDirectory }
        for (rules in listOf("backup_rules.xml", "data_extraction_rules.xml")) {
            val xml = File(dir, rules).readText()
            assertFalse(rules, xml.contains("otc_tour"))
            assertTrue(rules, xml.contains("<include domain=\"sharedpref\" path=\"otc_settings.xml\" />"))
        }
    }

    /** A rich text in English, as RichText.resolve builds it, its spans plain. */
    private fun RichText.english(): String {
        val (template, args) = when (this) {
            is RichText.Res -> EnglishStrings.string(id).first to args
            is RichText.Plural -> EnglishStrings.plural(id, count).first to args
        }
        val values = UiTextFormat.values(args, Locale.US, { n, l -> NumberFormat.getIntegerInstance(l).format(n) }) { it.english() }
        return UiTextFormat.rich(template, values, Locale.US, emptyMap()).text
    }

    /** A step's text in English, as TourView shows it. */
    private fun text(step: TourStep, wide: Boolean = false, faces: Boolean = false): String =
        when (val body = tourBody(step, wide, faces) { it.english() }) {
            is TourBody.Plain -> body.text.english()
            is TourBody.Rich -> listOfNotNull(body.text.english(), body.more?.english()).joinToString(" ")
        }

    @Test fun t14TheLabelsAreTheirOwnKeys() {
        assertEquals(
            "Press and hold a photo, then drag across others to select them too. Then choose Share, Download, Add to collection or Delete in the bar at the bottom.",
            text(SELECT),
        )
        assertEquals(
            "The button beside the folder's path switches between List, Grid and Storage, which shows what takes up space. Under Sort by, pick an order, and pick it again to reverse it.",
            text(FILES),
        )
        assertEquals(
            "Tap +, pick photos or videos from Phone (this phone) or Synced (already on your device), write a caption and tap Publish. It goes from your device to your friends' devices.",
            text(POST),
        )
        assertEquals(
            "This phone copies its photos and videos to your device by itself. Under Sync Options you choose what it sends, and Wi-Fi only keeps it off mobile data.",
            text(BACKUP),
        )
        assertEquals(
            "Give each person their own Off The Cloud on this same device: their own photos, files and private feed, separate from yours. In Settings › Users, type a username, tap Add and set their first password. Keep Request bridge access on to give them their own address.",
            text(USERS),
        )
        assertEquals(
            "Alerts counts what's new: likes, comments, friend requests and news from your device. Settings has your profile, password and language, and Show the tour to see this again.",
            text(ALERTS),
        )
        // The layout's own words.
        assertTrue(text(WELCOME).endsWith("in the bar at the bottom."))
        assertTrue(text(WELCOME, wide = true).endsWith("in the menu on the left."))
        assertTrue(text(PEOPLE, faces = true).startsWith("The button next to the search opens People"))
        assertTrue(text(SEARCH, faces = true).contains("someone's name"))
        assertFalse(text(SEARCH).contains("someone's name"))
        assertEquals("Step 3 of 13", S.appTourProgress(3, 13).english())
        // Every step has a text.
        for (step in TourStep.entries) {
            assertTrue(text(step, wide = false, faces = true).isNotEmpty())
            assertTrue(text(step, wide = true, faces = true).isNotEmpty())
        }
    }

    @Test fun t15EachStepShowsItsSectionButNeverAlerts() {
        val narrow = mapOf(
            WELCOME to null, SEARCH to Section.Images, DATES to Section.Images, SELECT to Section.Images, VIEWER to Section.Images,
            PEOPLE to Section.Images, COLLECTIONS to Section.Images, FILES to Section.Files, UPLOAD to Section.Files, SOCIAL to Section.Social,
            POST to Section.Social, FRIENDS to Section.Social, BACKUP to Section.Settings, USERS to Section.Settings, ALERTS to Section.Settings,
        )
        for ((step, section) in narrow) assertEquals(step.name, section, TourRules.section(step, wide = false))
        // The wide layout's pages, from the menu.
        assertEquals(Section.People, TourRules.section(PEOPLE, wide = true))
        assertEquals(Section.Collections, TourRules.section(COLLECTIONS, wide = true))
        assertEquals(Section.Friends, TourRules.section(FRIENDS, wide = true))
        assertEquals(Section.Images, TourRules.section(SEARCH, wide = true))
        // Opening Alerts marks every alert read: no step shows it.
        for (step in TourStep.entries) {
            assertTrue(TourRules.section(step, false) != Section.Alerts)
            assertTrue(TourRules.section(step, true) != Section.Alerts)
        }
    }

    @Test fun t16EachStepRingsItsOwnControls() {
        assertEquals(listOf(TourTarget.SECTIONS), TourRules.targets(WELCOME, false))
        assertEquals(listOf(TourTarget.SEARCH), TourRules.targets(SEARCH, false))
        assertEquals(listOf(TourTarget.SCRUBBER), TourRules.targets(DATES, true))
        assertEquals(listOf(TourTarget.FIRST_PHOTO), TourRules.targets(SELECT, false))
        assertEquals(listOf(TourTarget.PEOPLE_BUTTON), TourRules.targets(PEOPLE, false))
        assertEquals(listOf(TourTarget.FIRST_FACE), TourRules.targets(PEOPLE, true))
        assertEquals(listOf(TourTarget.COLLECTIONS_BUTTON), TourRules.targets(COLLECTIONS, false))
        assertEquals(listOf(TourTarget.FIRST_COLLECTION), TourRules.targets(COLLECTIONS, true))
        assertEquals(listOf(TourTarget.VIEW_OPTIONS), TourRules.targets(FILES, false))
        assertEquals(listOf(TourTarget.UPLOAD, TourTarget.FOLDER_LOCK), TourRules.targets(UPLOAD, false))
        assertEquals(listOf(TourTarget.LIKE, TourTarget.COMMENT), TourRules.targets(SOCIAL, false))
        assertEquals(listOf(TourTarget.NEW_POST), TourRules.targets(POST, true))
        assertEquals(listOf(TourTarget.FRIENDS_BUTTON), TourRules.targets(FRIENDS, false))
        assertEquals(listOf(TourTarget.ADDRESS, TourTarget.ADD_FRIEND), TourRules.targets(FRIENDS, true))
        assertEquals(listOf(TourTarget.WIFI_ONLY), TourRules.targets(BACKUP, false))
        assertEquals(listOf(TourTarget.ADD_USER, TourTarget.BRIDGE_ACCESS), TourRules.targets(USERS, false))
        // Show the tour too, where the card can keep clear of it.
        assertEquals(listOf(TourTarget.ALERTS_SECTION, TourTarget.SETTINGS_SECTION), TourRules.targets(ALERTS, false))
        assertEquals(listOf(TourTarget.ALERTS_SECTION, TourTarget.SETTINGS_SECTION, TourTarget.SHOW_TOUR), TourRules.targets(ALERTS, true))
        // Settings scrolls to what its steps ring: high on a narrow window, whose card is at the foot.
        assertEquals(TourTarget.WIFI_ONLY, TourRules.settingsScroll(BACKUP, false)?.first)
        assertEquals(TourTarget.ADD_USER, TourRules.settingsScroll(USERS, false)?.first)
        assertTrue((TourRules.settingsScroll(BACKUP, false)?.second ?: 1f) < 0.3f)
        assertEquals(TourTarget.SHOW_TOUR, TourRules.settingsScroll(ALERTS, false)?.first)
        assertEquals(TourTarget.SHOW_TOUR, TourRules.settingsScroll(ALERTS, true)?.first)
        assertNull(TourRules.settingsScroll(SEARCH, false))
    }

    @Test fun t17HolesArePaddedKeptInsideAndMergedWhenTheyTouch() {
        val bounds = Rect(0f, 0f, 400f, 800f)
        // Padded by 6.
        assertEquals(listOf(Rect(94f, 94f, 156f, 126f)), TourRules.holes(listOf(Rect(100f, 100f, 150f, 120f)), bounds))
        // Off screen: none; empty: none.
        assertTrue(TourRules.holes(listOf(Rect(500f, 100f, 550f, 120f)), bounds).isEmpty())
        assertTrue(TourRules.holes(listOf(Rect(10f, 10f, 10f, 30f)), bounds).isEmpty())
        // Kept 2 inside the window.
        assertEquals(listOf(Rect(2f, 754f, 398f, 798f)), TourRules.holes(listOf(Rect(0f, 760f, 400f, 800f)), bounds))
        // Two that touch are one; two apart stay two.
        assertEquals(listOf(Rect(4f, 4f, 101f, 56f)), TourRules.holes(listOf(Rect(10f, 10f, 50f, 50f), Rect(55f, 10f, 95f, 50f)), bounds))
        assertEquals(2, TourRules.holes(listOf(Rect(10f, 10f, 50f, 50f), Rect(200f, 300f, 240f, 340f)), bounds).size)
    }

    @Test fun t18TheCardKeepsOffTheRings() {
        val area = Rect(0f, 50f, 400f, 800f)
        val card = Size(380f, 300f)
        // A narrow window: across the foot, at most the window's width.
        assertEquals(Rect(12f, 488f, 388f, 788f), TourRules.cardFrame(emptyList(), card, area, sheet = true))
        // At the top when the foot would cover a ring (the bottom bar).
        val bar = listOf(Rect(0f, 740f, 400f, 800f))
        val top = TourRules.cardFrame(bar, card, area, sheet = true)
        assertEquals(62f, top.top)
        assertEquals(0f, TourRules.covered(top, bar))
        // A ring at the top leaves it at the foot.
        assertEquals(488f, TourRules.cardFrame(listOf(Rect(10f, 60f, 310f, 100f)), card, area, sheet = true).top)
        // Rings at the top and the foot: in the middle.
        val both = listOf(Rect(10f, 60f, 310f, 100f), Rect(0f, 740f, 400f, 800f))
        val middle = TourRules.cardFrame(both, card, area, sheet = true)
        assertEquals(275f, middle.top)
        assertEquals(0f, TourRules.covered(middle, both))
        // Taller than the window: cut to it.
        assertEquals(726f, TourRules.cardFrame(emptyList(), Size(380f, 2000f), area, sheet = true).height)
        // A wide window: in the middle with nothing rung, else beside the rings.
        val wideArea = Rect(0f, 0f, 1000f, 700f)
        assertEquals(Rect(310f, 200f, 690f, 500f), TourRules.cardFrame(emptyList(), card, wideArea, sheet = false))
        val left = listOf(Rect(20f, 100f, 220f, 160f))
        val beside = TourRules.cardFrame(left, card, wideArea, sheet = false)
        assertEquals(220f + TourRules.CARD_GAP, beside.left)
        assertEquals(0f, TourRules.covered(beside, left))
        // Rings everywhere: the corner that covers the least.
        val everywhere = listOf(Rect(0f, 0f, 1000f, 330f), Rect(0f, 370f, 600f, 700f))
        val corner = TourRules.cardFrame(everywhere, card, wideArea, sheet = false)
        assertEquals(988f, corner.right)
        assertEquals(688f, corner.bottom)
    }

    @Test fun t19TheUsersStepComesOnceTheOwnerIsKnown() = runBlocking {
        val owner = model(FakeStore(2), owner = true)
        assertNull(owner.owner.value)
        owner.learnOwner()
        assertEquals(true, owner.owner.value)
        assertTrue(USERS in TourRules.steps(false, owner.owner.value))
        TourModel.reset()
        val other = model(FakeStore(2), owner = false)
        other.learnOwner()
        assertEquals(false, other.owner.value)
        assertFalse(USERS in TourRules.steps(false, other.owner.value))
        TourModel.reset()
        // Not asked (no connection): stays unknown, and no Users step.
        val unknown = model(FakeStore(2), owner = null)
        unknown.learnOwner()
        assertNull(unknown.owner.value)
    }

}
