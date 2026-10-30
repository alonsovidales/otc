// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appPhotosMemoriesHidden
import cloud.offthe.otc.i18n.appPhotosMemoriesYearsAgo
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.GetMemories
import cloud.offthe.otc.proto.HideMemory
import cloud.offthe.otc.proto.Memories
import cloud.offthe.otc.proto.Memory
import cloud.offthe.otc.proto.MemoryHideKind
import cloud.offthe.otc.proto.MemoryPeriod
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.common.TestThumbStore
import com.google.protobuf.Timestamp
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.cancel
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.time.ZoneId
import java.time.ZonedDateTime
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.Executors

// On this day's rules and model (MemoriesModel.kt) against a pretend
// device - iOS's MemoriesTests, case for case.
class MemoriesModelTest {
    private val executor = Executors.newSingleThreadExecutor()
    private val dispatcher = executor.asCoroutineDispatcher()
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)
    @get:Rule val tmp = TemporaryFolder()
    private val thumbs by lazy { TestThumbStore(tmp.newFolder()) }

    @After fun tearDown() {
        scope.cancel()
        executor.shutdownNow()
    }

    private fun <T> on(block: suspend () -> T): T = runBlocking(dispatcher) { block() }

    private fun await(what: String, cond: () -> Boolean) {
        val until = System.nanoTime() + 5_000_000_000L
        while (!on { cond() }) {
            if (System.nanoTime() > until) throw AssertionError("timed out waiting for $what")
            Thread.sleep(5)
        }
    }

    private fun file(hash: String, seconds: Long = 1_698_660_000) =
        File.newBuilder().setHash(hash).setPath("/p/$hash.jpg").setMime("image/jpeg").setCreated(Timestamp.newBuilder().setSeconds(seconds)).build()

    private fun memory(key: String, years: Int, vararg hashes: String, people: List<String> = emptyList()): Memory {
        val files = hashes.map { file(it) }
        return Memory.newBuilder().setKey(key).setYearsAgo(years).setStartDate("2023-10-30").setEndDate("2023-10-30")
            .setCover(files.first()).addAllFiles(files).addAllPeople(people).setPhotoCount(files.size).build()
    }

    private fun answer(vararg m: Memory, measuring: Boolean = false, hidden: Int = 0) =
        Memories.newBuilder().addAllMemories(m.toList()).setMeasuring(measuring).setHiddenCount(hidden).build()

    // --- the rules ------------------------------------------------------

    @Test fun periodIsTheFirstWithMemoriesOnceTheEarlierOnesAnswered() {
        val none = answer()
        val some = answer(memory("day:2023-10-30", 3, "a"))
        val rules = MemoriesRules
        // Day not answered yet: nothing decided.
        assertNull(rules.period(null, mapOf(MemoryPeriodChoice.WEEK to some)))
        assertEquals(MemoryPeriodChoice.DAY, rules.period(null, mapOf(MemoryPeriodChoice.DAY to some)))
        // Day empty: the week; week not answered yet: still nothing.
        assertNull(rules.period(null, mapOf(MemoryPeriodChoice.DAY to none)))
        assertEquals(MemoryPeriodChoice.WEEK, rules.period(null, mapOf(MemoryPeriodChoice.DAY to none, MemoryPeriodChoice.WEEK to some)))
        assertEquals(
            MemoryPeriodChoice.MONTH,
            rules.period(null, mapOf(MemoryPeriodChoice.DAY to none, MemoryPeriodChoice.WEEK to none, MemoryPeriodChoice.MONTH to some)),
        )
        // None has any: hidden.
        assertNull(rules.period(null, mapOf(MemoryPeriodChoice.DAY to none, MemoryPeriodChoice.WEEK to none, MemoryPeriodChoice.MONTH to none)))
    }

    @Test fun aChoiceIsKeptWhileItHasMemories() {
        val none = answer()
        val some = answer(memory("week:2023-10-27", 3, "a"))
        val all = mapOf(MemoryPeriodChoice.DAY to some, MemoryPeriodChoice.WEEK to some, MemoryPeriodChoice.MONTH to none)
        assertEquals(MemoryPeriodChoice.WEEK, MemoriesRules.period(MemoryPeriodChoice.WEEK, all))
        // Picked, but empty (all hidden since): back to the default.
        assertEquals(MemoryPeriodChoice.DAY, MemoriesRules.period(MemoryPeriodChoice.MONTH, all))
    }

    @Test fun yearsAgoIsPlural() {
        assertEquals("1 year ago", S.appPhotosMemoriesYearsAgo(1).english())
        assertEquals("3 years ago", S.appPhotosMemoriesYearsAgo(3).english())
    }

    @Test fun theDaysAMemoryCovers() {
        assertEquals("30 Oct 2023", memoryDays("day:2023-10-30", "2023-10-30", "2023-10-30").english())
        assertEquals("27 Oct – 2 Nov 2023", memoryDays("week:2023-10-27", "2023-10-27", "2023-11-02").english())
        assertEquals("October 2023", memoryDays("month:2023-10", "2023-10-01", "2023-10-31").english())
        assertEquals("", memoryDays("day:x", "x", "x").english())
    }

    @Test fun onlyANameExactlyOnePersonHasCanBeHidden() {
        val people = listOf(
            Person.newBuilder().setId("p1").setName("Ana").build(),
            Person.newBuilder().setId("p2").setName("Leo").build(),
            Person.newBuilder().setId("p3").setName("Leo").build(),
        )
        assertEquals(listOf("p1"), MemoriesRules.hideablePeople(listOf("Leo", "Ana", "Eva"), people).map { it.id })
    }

    @Test fun aHiddenPhotoOrMemoryGoesAtOnce() {
        val m = answer(memory("day:2023-10-30", 3, "a", "b"), memory("day:2022-10-30", 4, "c"))
        val noA = MemoriesRules.withoutHidden(m, MemoryHideKind.MemoryHidePhoto, "a")
        assertEquals(listOf("b"), noA.memoriesList[0].filesList.map { it.hash })
        assertEquals("b", noA.memoriesList[0].cover.hash)
        // A memory's last photo hidden: the memory goes.
        assertEquals(listOf("day:2023-10-30"), MemoriesRules.withoutHidden(m, MemoryHideKind.MemoryHidePhoto, "c").memoriesList.map { it.key })
        assertEquals(listOf("day:2022-10-30"), MemoriesRules.withoutHidden(m, MemoryHideKind.MemoryHideMemory, "day:2023-10-30").memoriesList.map { it.key })
        // A day or a person waits for the device's answer.
        assertTrue(MemoriesRules.withoutHidden(m, MemoryHideKind.MemoryHideDay, "2023-10-30") === m)
    }

    @Test fun theDayAPhotoWasTakenIsInThePhonesZone() {
        // 2023-10-30 23:30 UTC is already the 31st in Madrid.
        val f = file("a", seconds = 1_698_708_600)
        assertEquals("2023-10-30", MemoriesRules.dayOf(f, ZoneId.of("UTC")))
        assertEquals("2023-10-31", MemoriesRules.dayOf(f, ZoneId.of("Europe/Madrid")))
        assertNull(MemoriesRules.dayOf(File.newBuilder().setHash("x").build(), ZoneId.of("UTC")))
    }

    @Test fun theNextDayStartsAtMidnight() {
        val t = ZonedDateTime.of(2026, 10, 30, 23, 59, 0, 0, ZoneId.of("Europe/Madrid"))
        assertEquals(61_000L, MemoriesDay.msToNextDay(t))
        val d = MemoriesDay.of(t)
        assertEquals("2026-10-30", d.date)
        assertEquals("Europe/Madrid", d.zone)
        assertEquals(60, d.offsetMinutes)
        val r = d.request(MemoryPeriodChoice.WEEK)
        assertEquals(MemoryPeriod.MemoryWeek, r.period)
        assertEquals("2026-10-30", r.date)
    }

    @Test fun aRefusalSaysTheDevicesWords() {
        assertNull(MemoriesRules.refusal(RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build()))
        val demo = RespEnvelope.newBuilder().setError(true).setErrorCode("demo").setErrorMessage("This is a demo").build()
        assertEquals("This is a demo", MemoriesRules.refusal(demo)?.english())
        assertEquals("Couldn't hide it. Try again.", MemoriesRules.refusal(null)?.english())
    }

    // --- the model ------------------------------------------------------

    /** The device: today's memories per period, and what it was asked. */
    private class Device {
        val asks = CopyOnWriteArrayList<GetMemories>()
        val hides = CopyOnWriteArrayList<HideMemory>()
        @Volatile var answers: Map<MemoryPeriod, Memories> = emptMap()
        @Volatile var errorCode = ""
        @Volatile var refuseHides = false
        /** The photos hidden: left out of what it answers, as the device does. */
        val hidden: MutableSet<String> = java.util.concurrent.ConcurrentHashMap.newKeySet()

        fun memories(r: GetMemories): RespEnvelope {
            asks += r
            if (errorCode.isNotEmpty()) return RespEnvelope.newBuilder().setError(true).setErrorCode(errorCode).setErrorMessage("no").build()
            val a = answers[r.period] ?: Memories.getDefaultInstance()
            var out = a
            for (h in hidden) out = MemoriesRules.withoutHidden(out, MemoryHideKind.MemoryHidePhoto, h)
            return RespEnvelope.newBuilder().setRespMemories(out).build()
        }

        fun hide(r: HideMemory): RespEnvelope {
            hides += r
            if (refuseHides) return RespEnvelope.newBuilder().setError(true).setErrorCode("demo").setErrorMessage("This is a demo").build()
            if (r.kind == MemoryHideKind.MemoryHidePhoto) { if (r.show) hidden -= r.value else hidden += r.value }
            return RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build()
        }

        companion object { fun emptMap() = emptyMap<MemoryPeriod, Memories>() }
    }

    private var turnedOff = 0

    private fun model(d: Device, now: () -> ZonedDateTime = { ZonedDateTime.of(2026, 10, 30, 12, 0, 0, 0, ZoneId.of("Europe/Madrid")) }) = MemoriesModel(
        scope,
        getMemories = { d.memories(it) },
        hideMemory = { d.hide(it) },
        bigThumbAsk = { RespEnvelope.getDefaultInstance() },
        thumbs = thumbs,
        sendThumbs = { RespEnvelope.getDefaultInstance() },
        pause = { },
        now = now,
        turnedOff = { turnedOff++ },
    )

    @Test fun asksTheThreePeriodsForTodayAndShowsTheFirstWithMemories() {
        val d = Device()
        d.answers = mapOf(MemoryPeriod.MemoryWeek to answer(memory("week:2023-10-27", 3, "a"), hidden = 2))
        val m = model(d)
        on { m.appeared() }
        await("three answers") { m.state.value.answers.size == 3 }
        val st = m.state.value
        assertEquals(listOf(MemoryPeriod.MemoryDay, MemoryPeriod.MemoryWeek, MemoryPeriod.MemoryMonth), d.asks.map { it.period }.take(3))
        assertEquals("2026-10-30", d.asks[0].date)
        assertEquals("Europe/Madrid", d.asks[0].timeZone)
        assertEquals(MemoryPeriodChoice.WEEK, st.period)
        assertTrue(st.visible)
        assertFalse(st.has(MemoryPeriodChoice.DAY))
        assertEquals(2, st.hiddenCount)
    }

    @Test fun nothingInAnyPeriodHidesTheStrip() {
        val d = Device()
        val m = model(d)
        on { m.appeared() }
        await("three answers") { m.state.value.answers.size == 3 }
        assertFalse(m.state.value.visible)
    }

    @Test fun memoriesOffOrAnOlderDeviceHidesItForGood() {
        for (code in listOf("memories_off", "unknown_payload")) {
            val d = Device()
            d.errorCode = code
            val before = turnedOff
            val m = model(d)
            on { m.appeared() }
            await("off for $code") { m.state.value.off }
            assertFalse(m.state.value.visible)
            assertEquals(before + 1, turnedOff)
            on { m.disappeared() }
        }
    }

    @Test fun aNewDayStartsAfresh() {
        val d = Device()
        d.answers = mapOf(MemoryPeriod.MemoryDay to answer(memory("day:2023-10-30", 3, "a")))
        var t = ZonedDateTime.of(2026, 10, 30, 23, 59, 0, 0, ZoneId.of("Europe/Madrid"))
        val m = model(d) { t }
        on { m.appeared() }
        await("today") { m.state.value.answers.size == 3 }
        d.answers = mapOf(MemoryPeriod.MemoryDay to answer(memory("day:2023-10-31", 3, "b")))
        t = t.plusMinutes(2)
        on { m.refresh() }
        await("tomorrow") { m.state.value.day?.date == "2026-10-31" && m.state.value.answers.size == 3 }
        assertEquals("day:2023-10-31", m.state.value.shown.single().key)
    }

    @Test fun hidingAPhotoTakesItAwayAndUndoShowsItAgain() {
        val d = Device()
        d.answers = mapOf(MemoryPeriod.MemoryDay to answer(memory("day:2023-10-30", 3, "a", "b")))
        val m = model(d)
        on { m.appeared() }
        await("answers") { m.state.value.answers.size == 3 }
        val said: UiText = S.appPhotosMemoriesHidden()
        assertTrue(on { m.hide(MemoryHideKind.MemoryHidePhoto, "a", said) })
        val n = m.state.value.notice!!
        assertEquals("Hidden from your memories", n.text.english())
        assertEquals(MemoryHideKind.MemoryHidePhoto, d.hides.single().kind)
        assertEquals("a", d.hides.single().value)
        fun shown() = m.state.value.answers[MemoryPeriodChoice.DAY]?.memoriesList?.singleOrNull()?.filesList?.map { it.hash }
        // Gone at once, and the device's answer after it agrees.
        assertEquals(listOf("b"), on { shown() })
        await("asked again") { d.asks.size >= 6 }
        assertEquals(listOf("b"), on { shown() })
        on { m.undo(n) }
        assertTrue(d.hides.last().show)
        assertEquals("a", d.hides.last().value)
        assertNull(m.state.value.notice)
        await("shown again") { shown() == listOf("a", "b") }
    }

    @Test fun aRefusedHideChangesNothingAndSaysWhy() {
        val d = Device()
        d.answers = mapOf(MemoryPeriod.MemoryDay to answer(memory("day:2023-10-30", 3, "a", "b")))
        d.refuseHides = true
        val m = model(d)
        on { m.appeared() }
        await("answers") { m.state.value.answers.size == 3 }
        assertFalse(on { m.hide(MemoryHideKind.MemoryHideMemory, "day:2023-10-30", S.appPhotosMemoriesHidden()) })
        val n = m.state.value.notice!!
        assertEquals("This is a demo", n.text.english())
        assertNull(n.undo)
        assertEquals(1, m.state.value.shown.size)
    }

    @Test fun theChoiceIsKeptForTheSession() {
        val d = Device()
        d.answers = mapOf(
            MemoryPeriod.MemoryDay to answer(memory("day:2023-10-30", 3, "a")),
            MemoryPeriod.MemoryMonth to answer(memory("month:2023-10", 3, "b")),
        )
        val m = model(d)
        on { m.appeared() }
        await("answers") { m.state.value.answers.size == 3 }
        assertEquals(MemoryPeriodChoice.DAY, m.state.value.period)
        on { m.choose(MemoryPeriodChoice.MONTH) }
        assertEquals(MemoryPeriodChoice.MONTH, m.state.value.period)
        on { m.disappeared(); m.appeared() }
        assertEquals(MemoryPeriodChoice.MONTH, m.state.value.period)
    }
}
