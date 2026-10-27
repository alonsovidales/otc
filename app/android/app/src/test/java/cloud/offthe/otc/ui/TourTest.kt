// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.i18n.EnglishStrings
import cloud.offthe.otc.i18n.RichText
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiTextFormat
import cloud.offthe.otc.i18n.appSettingsSyncTitle
import cloud.offthe.otc.i18n.appShellSectionSettings
import cloud.offthe.otc.i18n.appSocialComposePublish
import cloud.offthe.otc.i18n.appSocialFriendsAddHeading
import cloud.offthe.otc.i18n.appTourBackupText
import cloud.offthe.otc.i18n.appTourDoneText
import cloud.offthe.otc.i18n.appTourFriendsAdd
import cloud.offthe.otc.i18n.appTourPostStep3
import cloud.offthe.otc.i18n.appTourProgress
import cloud.offthe.otc.i18n.appTourShow
import cloud.offthe.otc.i18n.commonSend
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.ui.TourStep.ALERTS
import cloud.offthe.otc.ui.TourStep.BACKUP
import cloud.offthe.otc.ui.TourStep.COLLECTIONS
import cloud.offthe.otc.ui.TourStep.DONE
import cloud.offthe.otc.ui.TourStep.FILES
import cloud.offthe.otc.ui.TourStep.FRIENDS
import cloud.offthe.otc.ui.TourStep.IMAGES
import cloud.offthe.otc.ui.TourStep.PEOPLE
import cloud.offthe.otc.ui.TourStep.POST
import cloud.offthe.otc.ui.TourStep.SOCIAL
import cloud.offthe.otc.ui.TourStep.WELCOME
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

// The first-run tour's rules and model (Tour.kt): its pages, when it opens
// by itself, the mark kept on the phone, and the pages while People comes
// and goes. iOS's TourTests.swift has the same cases, under the same names
// (T1-T14).
class TourTest {
    private val all = listOf(WELCOME, IMAGES, BACKUP, PEOPLE, COLLECTIONS, FILES, SOCIAL, POST, FRIENDS, ALERTS, DONE)
    private val noFaces = listOf(WELCOME, IMAGES, BACKUP, COLLECTIONS, FILES, SOCIAL, POST, FRIENDS, ALERTS, DONE)

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
    }

    private fun model(store: TourStore): TourModel {
        TourModel.store = store
        return TourModel
    }

    /** Next as the pager does it: the page after the one shown, or closed on the last. */
    private fun next(steps: List<TourStep>) {
        val after = TourRules.after(TourRules.place(TourModel.step.value, steps), steps)
        if (after == null) TourModel.close() else TourModel.show(after)
    }

    private fun back(steps: List<TourStep>) {
        TourRules.before(TourRules.place(TourModel.step.value, steps), steps)?.let(TourModel::show)
    }

    @Test fun t1TheVersionIsOne() {
        assertEquals(1, TourRules.VERSION)
    }

    @Test fun t2PeopleOnlyWhileFaceRecognitionIsOn() {
        assertEquals(all, TourRules.steps(true))
        assertEquals(noFaces, TourRules.steps(false))
        // Not known yet: left out, as the menu does.
        assertEquals(noFaces, TourRules.steps(null))
        assertEquals(11, TourRules.steps(true).size)
        assertEquals(10, TourRules.steps(false).size)
    }

    @Test fun t3OpensByItselfOnlyWhenEverythingIsReady() {
        assertTrue(TourRules.shouldShow(0, syncChoicePending = false, signedIn = true, mainVisible = true))
        // Seen, this version or a later one.
        assertFalse(TourRules.shouldShow(1, syncChoicePending = false, signedIn = true, mainVisible = true))
        assertFalse(TourRules.shouldShow(2, syncChoicePending = false, signedIn = true, mainVisible = true))
        // The mark can't be read.
        assertFalse(TourRules.shouldShow(null, syncChoicePending = false, signedIn = true, mainVisible = true))
        // The sync question first.
        assertFalse(TourRules.shouldShow(0, syncChoicePending = true, signedIn = true, mainVisible = true))
        // Not signed in yet (or any more).
        assertFalse(TourRules.shouldShow(0, syncChoicePending = false, signedIn = false, mainVisible = true))
        // The connection screen, the Bluetooth setup, a card over MainView.
        assertFalse(TourRules.shouldShow(0, syncChoicePending = false, signedIn = true, mainVisible = false))
    }

    @Test fun t4NextAndBackFollowTheFixedOrder() {
        assertEquals(IMAGES, TourRules.after(WELCOME, all))
        assertEquals(PEOPLE, TourRules.after(BACKUP, all))
        assertEquals(COLLECTIONS, TourRules.after(BACKUP, noFaces))
        assertEquals(DONE, TourRules.after(ALERTS, all))
        assertNull(TourRules.after(DONE, all))
        assertNull(TourRules.before(WELCOME, all))
        assertEquals(WELCOME, TourRules.before(IMAGES, all))
        assertEquals(PEOPLE, TourRules.before(COLLECTIONS, all))
        assertEquals(BACKUP, TourRules.before(COLLECTIONS, noFaces))
        assertEquals(ALERTS, TourRules.before(DONE, noFaces))
    }

    @Test fun t5APageThatWentMovesToTheNextOne() {
        assertEquals(COLLECTIONS, TourRules.place(PEOPLE, noFaces))
        assertEquals(PEOPLE, TourRules.place(PEOPLE, all))
        assertEquals(FILES, TourRules.place(FILES, noFaces))
        // From a page that went, Next and Back go on from where it was.
        assertEquals(COLLECTIONS, TourRules.after(PEOPLE, noFaces))
        assertEquals(BACKUP, TourRules.before(PEOPLE, noFaces))
    }

    @Test fun t6OpensOnceAndIsMarkedSeenAsItOpens() {
        val store = FakeStore(0)
        val tour = model(store)
        tour.evaluate(syncChoicePending = false, signedIn = true, mainVisible = true)
        assertTrue(tour.shown.value)
        assertEquals(WELCOME, tour.step.value)
        assertEquals(1, store.value)
        tour.close()
        // A relaunch, another sign-in: seen.
        tour.evaluate(syncChoicePending = false, signedIn = true, mainVisible = true)
        assertFalse(tour.shown.value)
        assertEquals(listOf(1), store.writes)
    }

    @Test fun t7WaitsForTheSyncQuestionWithoutMarkingIt() {
        val store = FakeStore(0)
        val tour = model(store)
        tour.evaluate(syncChoicePending = true, signedIn = true, mainVisible = true)
        tour.evaluate(syncChoicePending = false, signedIn = false, mainVisible = true)
        tour.evaluate(syncChoicePending = false, signedIn = true, mainVisible = false)
        assertFalse(tour.shown.value)
        assertTrue(store.writes.isEmpty())
        tour.evaluate(syncChoicePending = false, signedIn = true, mainVisible = true)
        assertTrue(tour.shown.value)
    }

    @Test fun t8AMarkThatCantBeReadOrKeptOpensNothing() {
        val unreadable = FakeStore(null)
        model(unreadable).evaluate(syncChoicePending = false, signedIn = true, mainVisible = true)
        assertFalse(TourModel.shown.value)
        assertTrue(unreadable.writes.isEmpty())

        val unwritable = FakeStore(0).apply { writable = false }
        model(unwritable).evaluate(syncChoicePending = false, signedIn = true, mainVisible = true)
        assertFalse(TourModel.shown.value)
        assertEquals(listOf(1), unwritable.writes)
    }

    @Test fun t9ReplayStartsAtTheFirstPageAndLeavesTheMark() {
        val store = FakeStore(1)
        val tour = model(store)
        tour.replay()
        assertTrue(tour.shown.value)
        assertEquals(WELCOME, tour.step.value)
        next(all)
        next(all)
        assertEquals(BACKUP, tour.step.value)
        tour.close()
        tour.replay()
        assertEquals(WELCOME, tour.step.value)
        assertTrue(store.writes.isEmpty())
        assertEquals(1, store.value)
    }

    @Test fun t10NextWalksToDoneAndDoneCloses() {
        val tour = model(FakeStore(1))
        tour.replay()
        back(noFaces)
        assertEquals(WELCOME, tour.step.value)
        repeat(9) { next(noFaces) }
        assertEquals(DONE, tour.step.value)
        assertTrue(tour.shown.value)
        back(noFaces)
        assertEquals(ALERTS, tour.step.value)
        next(noFaces)
        next(noFaces)
        assertFalse(tour.shown.value)
    }

    @Test fun t11PeopleGoingWhileShownMovesOn() {
        val tour = model(FakeStore(1))
        tour.replay()
        repeat(3) { next(all) }
        assertEquals(PEOPLE, tour.step.value)
        // Face recognition turned off meanwhile: the page shown is the next.
        assertEquals(COLLECTIONS, TourRules.place(tour.step.value, noFaces))
        next(noFaces)
        assertEquals(FILES, tour.step.value)
        back(noFaces)
        back(noFaces)
        assertEquals(BACKUP, tour.step.value)
    }

    @Test fun t12LogOutClosesItAndTheMarkStays() {
        val store = FakeStore(0)
        val tour = model(store)
        tour.evaluate(syncChoicePending = false, signedIn = true, mainVisible = true)
        next(all)
        tour.reset()
        assertFalse(tour.shown.value)
        assertEquals(WELCOME, tour.step.value)
        // The next sign-in, on this device or another: not again.
        tour.evaluate(syncChoicePending = false, signedIn = true, mainVisible = true)
        assertFalse(tour.shown.value)
        assertEquals(1, store.value)
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

    @Test fun t14TheLabelsAreTheirOwnKeys() {
        assertEquals(
            "This phone can copy its photos and videos to your device by itself. Choose what it sends in Settings › Sync Options.",
            S.appTourBackupText(S.appShellSectionSettings().english(), S.appSettingsSyncTitle().english()).english(),
        )
        assertEquals("Write a caption, then tap Publish.", S.appTourPostStep3(S.appSocialComposePublish().english()).english())
        assertEquals(
            "To add a friend, type their device's name, the part before .off-the.cloud, under Add a friend and tap Send.",
            S.appTourFriendsAdd(SecretsStore.bridgeDomain, S.appSocialFriendsAddHeading().english(), S.commonSend().english()).english(),
        )
        assertEquals(
            "See this tour again any time: tap Show the tour in Settings.",
            S.appTourDoneText(S.appTourShow().english(), S.appShellSectionSettings().english()).english(),
        )
        assertEquals("Step 3 of 11", S.appTourProgress(3, 11).english())
    }
}
