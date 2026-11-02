// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appSettingsStorageConfirm
import cloud.offthe.otc.i18n.appSettingsStorageConfirmTitle
import cloud.offthe.otc.i18n.appSettingsStorageFailed
import cloud.offthe.otc.i18n.appSettingsStorageLookAlike
import cloud.offthe.otc.i18n.appSettingsStorageNewDisk
import cloud.offthe.otc.i18n.appSettingsStorageTitle
import cloud.offthe.otc.i18n.appSettingsStorageUse
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.MirrorDiskCandidate
import cloud.offthe.otc.proto.RaidState
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.Status
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.IOException
import java.util.Locale

// Settings › Storage (issue #208, StorageSection.kt): a disk that isn't
// blank offered for the mirror, as the web's MirrorDiskPanel. iOS's
// StorageSectionTests is the same, case for case.
class StorageSectionTest {
    private val en = Locale.forLanguageTag("en")
    private val gb = 1_000_000_000L

    private fun size(bytes: Long) = MirrorDisks.sizeText(bytes, en)

    @Test fun sizesAreDecimalWithAtMostOneDecimal() {
        assertEquals("512.1 GB", size(512_110_190_592))
        assertEquals("500 GB", size(500 * gb))
        assertEquals("2 TB", size(2_000 * gb))
        assertEquals("4 TB", size(4_000_787_030_016))
        assertEquals("1.5 TB", size(1_500 * gb))
        assertEquals("31.9 GB", size(31_914_983_424))
        assertEquals("64 GB", size(64_023_257_088))
        assertEquals("250.1 GB", size(250_059_350_016))
        // A size that rounds up to 1000 of a unit is one of the next.
        assertEquals("1 TB", size(999_960_000_000))
        assertEquals("999.9 GB", size(999_940_000_000))
        // Small ones, for completeness.
        assertEquals("0 B", size(0))
        assertEquals("999 B", size(999))
        assertEquals("1 kB", size(1_000))
        assertEquals("16.4 MB", size(16_400_000))
        assertEquals("0 B", size(-5))
        // The app's language's decimal mark.
        assertEquals("512,1 GB", MirrorDisks.sizeText(512_110_190_592, Locale.forLanguageTag("es")))
    }

    @Test fun aDiskIsNamedByItsModelAndSizeOrItsSizeAlone() {
        assertEquals("Generic Card Reader · 512.1 GB", MirrorDisk("a", "Generic Card Reader", 512_110_190_592).name(en))
        assertEquals("2 TB", MirrorDisk("a", "", 2_000 * gb).name(en))
        assertEquals("2 TB", MirrorDisk("a", "   ", 2_000 * gb).name(en))
        assertEquals("Samsung PSSD T7 · 1 TB", MirrorDisk("a", " Samsung PSSD T7 ", 1_000 * gb).name(en))
    }

    @Test fun twoDisksOfOneModelAndSizeGetNoButton() {
        val a = MirrorDisk("a", "Generic Card Reader", 64 * gb)
        val b = MirrorDisk("b", "Generic Card Reader", 64 * gb)
        val c = MirrorDisk("c", "Generic Card Reader", 128 * gb)
        val d = MirrorDisk("d", "Samsung PSSD T7", 64 * gb)
        val offered = listOf(a, b, c, d)
        assertTrue(MirrorDisks.lookAlike(a, offered))
        assertTrue(MirrorDisks.lookAlike(b, offered))
        // Another size, or another name: a button each.
        assertFalse(MirrorDisks.lookAlike(c, offered))
        assertFalse(MirrorDisks.lookAlike(d, offered))
        // Alone, never.
        assertFalse(MirrorDisks.lookAlike(a, listOf(a)))
        // Two nameless disks of one size can't be told apart either.
        val e = MirrorDisk("e", "", 64 * gb)
        val f = MirrorDisk("f", "", 64 * gb)
        assertTrue(MirrorDisks.lookAlike(e, listOf(e, f)))
    }

    @Test fun theSectionShowsOnlyWithSomethingToSay() {
        val d = MirrorDisk("a", "X", gb)
        assertFalse(MirrorDisks.shown(emptyList(), null, null))
        assertTrue(MirrorDisks.shown(listOf(d), null, null))
        assertTrue(MirrorDisks.shown(emptyList(), MirrorDiskAsked(d, 0), null))
        assertTrue(MirrorDisks.shown(emptyList(), null, d))
    }

    @Test fun aDiskAskedForIsFollowedThroughTheStatus() {
        val d = MirrorDisk("a", "X", gb)
        val t0 = 1_000_000L
        val waiting = MirrorDiskAsked(d, t0)
        // Still degraded a moment later: waiting.
        assertEquals(MirrorDiskStep.UNCHANGED, MirrorDisks.step(waiting, RaidState.RaidDegraded, t0 + 5_000))
        // The mirror copies onto it.
        assertEquals(MirrorDiskStep.COPYING, MirrorDisks.step(waiting, RaidState.RaidSyncing, t0 + 5_000))
        val copying = MirrorDiskAsked(d, t0, copying = true)
        assertEquals(MirrorDiskStep.UNCHANGED, MirrorDisks.step(copying, RaidState.RaidSyncing, t0 + 600_000))
        // Whole: done. A copy that stopped: done too (its Alert says why).
        assertEquals(MirrorDiskStep.DONE, MirrorDisks.step(copying, RaidState.RaidInSync, t0 + 900_000))
        assertEquals(MirrorDiskStep.DONE, MirrorDisks.step(copying, RaidState.RaidDegraded, t0 + 900_000))
        assertEquals(MirrorDiskStep.DONE, MirrorDisks.step(waiting, RaidState.RaidInSync, t0 + 3_000))
        // Nothing copying 20 s after the Ack: it wasn't added.
        assertEquals(MirrorDiskStep.UNCHANGED, MirrorDisks.step(waiting, RaidState.RaidDegraded, t0 + 19_900))
        assertEquals(MirrorDiskStep.NOT_ADDED, MirrorDisks.step(waiting, RaidState.RaidDegraded, t0 + 20_000))
        assertEquals(MirrorDiskStep.NOT_ADDED, MirrorDisks.step(waiting, RaidState.RaidUnknown, t0 + 25_000))
    }

    @Test fun theDevicesAnswers() {
        assertEquals(MirrorDiskAnswer.Taken, MirrorDiskAnswer.of(RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build()))
        val words = "That disk isn't offered any more."
        val refused = RespEnvelope.newBuilder().setError(true).setErrorCode("not_candidate").setErrorMessage(words)
            .setRespAck(Ack.newBuilder().setCode("not_candidate").setErrorMsg(words)).build()
        assertEquals(MirrorDiskAnswer.Refused(words), MirrorDiskAnswer.of(refused))
        val ackOnly = RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setErrorMsg("Another disk was just asked for.")).build()
        assertEquals(MirrorDiskAnswer.Refused("Another disk was just asked for."), MirrorDiskAnswer.of(ackOnly))
        assertEquals(MirrorDiskAnswer.Refused(null), MirrorDiskAnswer.of(RespEnvelope.getDefaultInstance()))
    }

    @Test fun everyTextReadsAsTheWebs() {
        assertEquals("Storage", S.appSettingsStorageTitle().english())
        assertEquals("New disk found: ⁨2 TB⁩. It has data on it, so it wasn't added to the mirror.", S.appSettingsStorageNewDisk("2 TB").english())
        assertEquals("Use it for the mirror", S.appSettingsStorageUse().english())
        assertEquals("Erase this disk and use it for the mirror?", S.appSettingsStorageConfirmTitle().english())
        assertEquals("Erase and use it", S.appSettingsStorageConfirm().english())
        assertEquals("Another disk plugged in looks just like this one, so neither can be chosen here: unplug the one to keep first.", S.appSettingsStorageLookAlike().english())
        assertEquals("The disk could not be used for the mirror.", S.appSettingsStorageFailed().english())
    }

    // The view model Settings feeds with its Status answers.

    private val disk = MirrorDisk("d1", "Generic Card Reader", 64 * gb)

    private fun status(state: RaidState, percent: Float = 0f, offering: List<MirrorDisk> = emptyList()): Status =
        Status.newBuilder().setRaidState(state).setRaidSyncPercent(percent)
            .addAllMirrorCandidates(offering.map { MirrorDiskCandidate.newBuilder().setId(it.id).setModel(it.model).setSize(it.size).build() })
            .build()

    private val ok: RespEnvelope = RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build()

    @Test fun offeredDisksComeInTheDevicesOrder() {
        val other = MirrorDisk("d2", "", 2_000 * gb)
        assertEquals(listOf(disk, other), MirrorDisks.offered(status(RaidState.RaidDegraded, offering = listOf(disk, other))))
        assertTrue(MirrorDisks.offered(null).isEmpty())
    }

    @Test fun afterTheAckItWaitsThenFollowsTheCopyToTheEnd() = runBlocking {
        val sent = mutableListOf<ReqEnvelope>()
        var now = 1_000_000L
        val vm = MirrorDiskViewModel({ build -> sent += ReqEnvelope.newBuilder().also(build).build(); ok }, { now })
        vm.confirm(disk)
        vm.use(disk)
        assertEquals("d1", sent.single().reqUseMirrorDisk.disk)
        var st = vm.state.value
        assertEquals(MirrorDiskAsked(disk, now), st.asked)
        assertNull(st.confirming)
        assertNull(st.failure)
        assertFalse(st.sending)
        // The device takes it up: Status every 2 s meanwhile.
        assertTrue(st.fastPoll)

        now += 4_000
        vm.sawStatus(status(RaidState.RaidDegraded))
        assertEquals(false, vm.state.value.asked?.copying)

        now += 2_000
        vm.sawStatus(status(RaidState.RaidSyncing, percent = 3f))
        st = vm.state.value
        assertEquals(true, st.asked?.copying)
        assertFalse(st.fastPoll)

        now += 3_600_000
        vm.sawStatus(status(RaidState.RaidSyncing, percent = 80f))
        assertEquals(true, vm.state.value.asked?.copying)

        vm.sawStatus(status(RaidState.RaidInSync))
        assertNull(vm.state.value.asked)
        assertNull(vm.state.value.notAdded)
    }

    @Test fun nothingCopyingAfterTwentySecondsWasntAdded() = runBlocking {
        var now = 1_000_000L
        val vm = MirrorDiskViewModel({ ok }, { now })
        vm.use(disk)

        now += 10_000
        vm.sawStatus(status(RaidState.RaidDegraded, offering = listOf(disk)))
        assertTrue(vm.state.value.asked != null)
        now += 10_000
        vm.sawStatus(status(RaidState.RaidDegraded, offering = listOf(disk)))
        assertNull(vm.state.value.asked)
        assertEquals(disk, vm.state.value.notAdded)
        assertFalse(vm.state.value.fastPoll)

        // Asked again and taken: the note goes.
        vm.use(disk)
        assertNull(vm.state.value.notAdded)
        assertTrue(vm.state.value.asked != null)
    }

    @Test fun aRefusalSaysTheDevicesWordsOrOurs() = runBlocking {
        val refusal = RespEnvelope.newBuilder().setError(true).setErrorCode("busy").setErrorMessage("Another disk was just asked for.").build()
        var answers = 0
        val vm = MirrorDiskViewModel({ if (++answers == 1) refusal else refusal.toBuilder().setErrorMessage("").build() }, { 0L })
        vm.use(disk)
        assertNull(vm.state.value.asked)
        assertEquals(UiText.Raw("Another disk was just asked for."), vm.state.value.failure)
        assertFalse(vm.state.value.sending)

        vm.use(disk)
        assertEquals("The disk could not be used for the mirror.", vm.state.value.failure?.english())
    }

    @Test fun noAnswerSaysWhy() = runBlocking {
        val vm = MirrorDiskViewModel({ throw IOException("The connection went.") }, { 0L })
        vm.use(disk)
        assertNull(vm.state.value.asked)
        assertEquals("The connection went.", vm.state.value.failure?.english())
    }

    @Test fun aStatusWithNothingAskedChangesNothing() {
        val vm = MirrorDiskViewModel({ throw IOException() }, { 0L })
        vm.sawStatus(status(RaidState.RaidSyncing))
        vm.sawStatus(null)
        assertNull(vm.state.value.asked)
        assertNull(vm.state.value.notAdded)
    }
}
