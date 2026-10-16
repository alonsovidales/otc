// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import androidx.compose.ui.geometry.Offset
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import kotlinx.coroutines.runBlocking
import cloud.offthe.otc.i18n.english
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// The People page's choices, as the web's PeopleView makes them.
class PeopleViewTest {
    private fun p(id: String, name: String, n: Int) = Person.newBuilder().setId(id).setName(name).setFaceCount(n).build()

    @Test fun namedFirstThenUnnamedEachByPhotos() {
        val all = listOf(p("a", "", 50), p("b", "Ana", 3), p("c", "  ", 9), p("d", "Leo", 12), p("e", "Rosa", 3))
        assertEquals(listOf("d", "b", "e", "a", "c"), peopleOrder(all, emptySet()).map { it.id })
        // Merged away or deleted: hidden before the list comes again.
        assertEquals(listOf("b", "e", "a", "c"), peopleOrder(all, setOf("d")).map { it.id })
    }

    @Test fun namesOnceWhateverTheirCaseOrSpaces() {
        assertEquals(listOf("Ana", "Leo"), namesOf(listOf(p("1", "Ana", 1), p("2", "ana ", 1), p("3", "", 1), p("4", " Leo", 1))))
    }

    @Test fun keepTheOnlyNamedOne() {
        assertEquals("b", firstKeep(listOf(p("a", "", 40), p("b", "Ana", 2), p("c", "", 9))))
    }

    @Test fun keepTheMostPhotosAmongOneName() {
        assertEquals("c", firstKeep(listOf(p("a", "", 40), p("b", "Ana", 2), p("c", "ana", 9))))
    }

    @Test fun keepTheMostPhotosWhenNobodyIsNamed() {
        assertEquals("b", firstKeep(listOf(p("a", "", 4), p("b", "", 40), p("c", "", 9))))
    }

    @Test fun differentNamesNeedAPick() {
        assertNull(firstKeep(listOf(p("a", "Ana", 4), p("b", "Leo", 40))))
    }

    // Issue #200: a long press starts a drag to select only on the face
    // itself. A cell 150 px wide with a 110 px face centred at its top
    // (20 px each side) and a 32 px "more" button standing 4 px out of its
    // top end corner (28 px of it over the face).
    @Test fun aDragStartsOnlyOnTheFace() {
        fun on(x: Float, y: Float, more: Float = 32f, rtl: Boolean = false) = onFace(Offset(x, y), 150f, 110f, more, 4f, rtl)
        assertTrue(on(75f, 55f))
        assertTrue(on(20f, 0f))
        assertTrue(on(129.9f, 109.9f))
        // The name (or "Add a name") and the count under it.
        assertFalse(on(75f, 110f))
        assertFalse(on(75f, 125f))
        assertFalse(on(75f, 150f))
        // Beside the face, in the cell.
        assertFalse(on(10f, 55f))
        assertFalse(on(140f, 55f))
        assertFalse(on(75f, -1f))
        // The "more" button, at the top end corner; under it the face again.
        assertFalse(on(120f, 10f))
        assertFalse(on(103f, 27f))
        assertTrue(on(101f, 10f))
        assertTrue(on(120f, 30f))
        // While picking there is no button.
        assertTrue(on(120f, 10f, more = 0f))
        // Right to left, the button is at the top left.
        assertFalse(on(25f, 10f, rtl = true))
        assertTrue(on(120f, 10f, rtl = true))
        // A cell narrower than the face: the face fills it.
        assertTrue(onFace(Offset(1f, 50f), 100f, 110f, 0f, 4f, false))
    }
}

// The merge and the deletion against a stand-in device: what is sent, in
// what order, and what the page shows after.
class PeopleViewModelTest {
    private fun p(id: String, name: String, n: Int) = Person.newBuilder().setId(id).setName(name).setFaceCount(n).build()
    private fun ack(ok: Boolean) = RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(ok)).build()

    private class Store : PeopleStore {
        val renamed = mutableListOf<Pair<String, String>>()
        val forgotten = mutableListOf<Set<String>>()
        override suspend fun loadPeople() = true
        override fun renamePersonLocally(id: String, name: String) { renamed += id to name }
        override fun forgetPeople(ids: Set<String>) { forgotten += ids }
    }

    /** A device answering each request with [answer] (null: no answer), recording what came. */
    private class Device(val answer: (ReqEnvelope) -> RespEnvelope?) {
        val sent = mutableListOf<ReqEnvelope>()
        val send: PeopleRequest = { build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            synchronized(sent) { sent += req }
            answer(req) ?: throw java.io.IOException("no answer")
        }
    }

    private fun picked(vm: PeopleViewModel, vararg ids: String) = ids.forEach { vm.pick(it) }

    // Issue #200: a drag to select whose pressed face goes away (a new
    // listing) ends there, the picks as they stood.
    @Test fun aDragEndsWhenThePressedFaceGoes() {
        val vm = PeopleViewModel(Device { ack(true) }.send)
        picked(vm, "z")
        val order = listOf("a", "b", "c", "d")
        vm.beginDragSelect("b", order)
        assertEquals(listOf("z", "b"), vm.state.value.selection)
        assertTrue(vm.dragSelect("d", order))
        assertEquals(listOf("z", "b", "c", "d"), vm.state.value.selection)
        // "c", under the finger, goes: nothing changes, the drag goes on.
        val noC = listOf("a", "b", "d")
        assertTrue(vm.dragSelect("c", noC))
        assertEquals(listOf("z", "b", "c", "d"), vm.state.value.selection)
        assertTrue(vm.dragSelect("a", noC))
        assertEquals(listOf("z", "a", "b"), vm.state.value.selection)
        // "b", the pressed one, goes with a new listing.
        vm.keepOnly(setOf("z", "a", "d"))
        assertEquals(listOf("z", "a"), vm.state.value.selection)
        assertFalse(vm.dragSelect("d", listOf("a", "d")))
        assertEquals(listOf("z", "a"), vm.state.value.selection)
        assertFalse(vm.dragSelect("a", listOf("a", "d")))
        vm.endDragSelect()
        assertEquals(listOf("z", "a"), vm.state.value.selection)
        assertFalse(vm.dragSelect("d", listOf("a", "d")))
    }

    // Issue #200: a face picked before the drag that goes with a refresh
    // (merged or deleted elsewhere) doesn't come back as the range moves on.
    @Test fun aFaceGoneMidDragIsNotPickedAgain() {
        val vm = PeopleViewModel(Device { ack(true) }.send)
        picked(vm, "x")
        val order = listOf("x", "a", "b", "c", "d")
        vm.beginDragSelect("b", order)
        assertTrue(vm.dragSelect("c", order))
        assertEquals(listOf("x", "b", "c"), vm.state.value.selection)
        // "x" goes: the page's refresh drops it from the picks.
        val noX = listOf("a", "b", "c", "d")
        vm.keepOnly(noX.toSet())
        assertEquals(listOf("b", "c"), vm.state.value.selection)
        assertTrue(vm.dragGoesOn(noX))
        assertTrue(vm.dragSelect("d", noX))
        assertEquals(listOf("b", "c", "d"), vm.state.value.selection)
        assertTrue(vm.dragSelect("a", noX))
        assertEquals(listOf("a", "b"), vm.state.value.selection)
        // The pressed face goes while the finger stays over "a": over at once.
        val noB = listOf("a", "c", "d")
        assertFalse(vm.dragGoesOn(noB))
        assertFalse(vm.dragGoesOn(noX))
        vm.endDragSelect()
        assertFalse(vm.dragGoesOn(noX))
    }

    @Test fun mergeNamesTheKeptFaceFirstThenMerges() = runBlocking {
        val dev = Device { ack(true) }
        val vm = PeopleViewModel(dev.send)
        val store = Store()
        picked(vm, "a", "b", "c")
        vm.askMerge()
        assertTrue(vm.mergeNow(store, p("b", "", 5), "Ana"))
        assertEquals(listOf(ReqEnvelope.PayloadCase.REQ_RENAME_PERSON, ReqEnvelope.PayloadCase.REQ_MERGE_PEOPLE), dev.sent.map { it.payloadCase })
        assertEquals("b", dev.sent[0].reqRenamePerson.id)
        assertEquals("Ana", dev.sent[0].reqRenamePerson.name)
        assertEquals("b", dev.sent[1].reqMergePeople.targetId)
        assertEquals(listOf("a", "c"), dev.sent[1].reqMergePeople.sourceIdsList)
        assertEquals(listOf("b" to "Ana"), store.renamed)
        assertEquals(listOf(setOf("a", "c")), store.forgotten)
        val st = vm.state.value
        assertEquals(emptyList<String>(), st.selection)
        assertNull(st.confirm)
        assertEquals(setOf("a", "c"), st.gone)
        assertEquals("Merged 3 into \u2068Ana\u2069", st.toast?.text?.english())
    }

    @Test fun mergeKeepingANamedFaceSendsNoRename() = runBlocking {
        val dev = Device { ack(true) }
        val vm = PeopleViewModel(dev.send)
        picked(vm, "a", "b")
        assertTrue(vm.mergeNow(Store(), p("a", "Leo", 5), "Leo"))
        assertEquals(listOf(ReqEnvelope.PayloadCase.REQ_MERGE_PEOPLE), dev.sent.map { it.payloadCase })
        assertEquals("Merged 2 into \u2068Leo\u2069", vm.state.value.toast?.text?.english())
    }

    @Test fun aRefusedNameMergesNothing() = runBlocking {
        val dev = Device { ack(it.payloadCase != ReqEnvelope.PayloadCase.REQ_RENAME_PERSON) }
        val vm = PeopleViewModel(dev.send)
        val store = Store()
        picked(vm, "a", "b")
        vm.askMerge()
        assertFalse(vm.mergeNow(store, p("a", "", 5), "Ana"))
        assertEquals(listOf(ReqEnvelope.PayloadCase.REQ_RENAME_PERSON), dev.sent.map { it.payloadCase })
        val st = vm.state.value
        assertEquals("The name couldn't be saved, so nothing was merged. Try again.", st.confirmError?.english())
        assertEquals(PeopleViewModel.Confirm.Merge, st.confirm)
        assertEquals(listOf("a", "b"), st.selection)
        assertTrue(store.forgotten.isEmpty())
    }

    @Test fun anUnansweredMergeSaysSo() = runBlocking {
        val vm = PeopleViewModel(Device { null }.send)
        picked(vm, "a", "b")
        assertFalse(vm.mergeNow(Store(), p("a", "Leo", 5), "Leo"))
        assertEquals("Your device didn't answer. Try again.", vm.state.value.confirmError?.english())
        assertFalse(vm.state.value.busy)
    }

    @Test fun whatCouldNotBeDeletedStaysSelected() = runBlocking {
        val dev = Device { ack(it.reqDeletePerson.id != "b") }
        val vm = PeopleViewModel(dev.send)
        val store = Store()
        picked(vm, "a", "b", "c", "d")
        vm.askDelete(listOf("a", "b", "c", "d"))
        assertTrue(vm.removeNow(store, listOf("a", "b", "c", "d")))
        assertEquals(setOf("a", "b", "c", "d"), dev.sent.map { it.reqDeletePerson.id }.toSet())
        assertEquals(listOf(setOf("a", "c", "d")), store.forgotten)
        val st = vm.state.value
        assertEquals(listOf("b"), st.selection)
        assertNull(st.confirm)
        assertNull(st.progress)
        assertEquals("1 of 4 people couldn't be deleted and is still selected. Try again.", st.toast?.text?.english())
        assertTrue(st.toast!!.error)
    }

    @Test fun nothingDeletedKeepsTheQuestionOpen() = runBlocking {
        val vm = PeopleViewModel(Device { null }.send)
        val store = Store()
        picked(vm, "a", "b")
        vm.askDelete(listOf("a", "b"))
        assertFalse(vm.removeNow(store, listOf("a", "b")))
        val st = vm.state.value
        assertEquals("Your device didn't answer. Try again.", st.confirmError?.english())
        assertEquals(PeopleViewModel.Confirm.Delete(listOf("a", "b")), st.confirm)
        assertEquals(listOf("a", "b"), st.selection)
        assertTrue(store.forgotten.isEmpty())
    }

    @Test fun oneDeletedSaysSo() = runBlocking {
        val vm = PeopleViewModel(Device { ack(true) }.send)
        vm.askDelete(listOf("a"))
        assertTrue(vm.removeNow(Store(), listOf("a")))
        assertEquals("Person deleted", vm.state.value.toast?.text?.english())
    }

    // A demo device refuses every change in its own words (error_code
    // "demo"): those are what the page says, not ours. The page offers no
    // naming, merging or deleting there, but a refusal is never left unsaid.
    private val demoSaid = "This is a demo device: changes are turned off."
    private fun demoRefusal() = RespEnvelope.newBuilder().setError(true).setErrorCode("demo").setErrorMessage(demoSaid)
        .setRespAck(Ack.newBuilder().setOk(false).setErrorMsg(demoSaid).setCode("demo")).build()

    @Test fun aDemoDeviceRefusingANameSaysSoInItsWords() = runBlocking {
        val vm = PeopleViewModel(Device { demoRefusal() }.send)
        val store = Store()
        vm.renameNow(store, "a", "Ana")
        assertTrue(store.renamed.isEmpty())
        assertEquals(demoSaid, vm.state.value.toast?.text?.english())
        assertTrue(vm.state.value.toast!!.error)
    }

    @Test fun aDemoDeviceRefusingAMergeOrADeleteSaysSoInItsWords() = runBlocking {
        val vm = PeopleViewModel(Device { demoRefusal() }.send)
        val store = Store()
        picked(vm, "a", "b")
        vm.askMerge()
        assertFalse(vm.mergeNow(store, p("a", "Leo", 5), "Leo"))
        assertEquals(demoSaid, vm.state.value.confirmError?.english())
        vm.dismissConfirm()
        vm.askDelete(listOf("a", "b"))
        assertFalse(vm.removeNow(store, listOf("a", "b")))
        assertEquals(demoSaid, vm.state.value.confirmError?.english())
        assertEquals(listOf("a", "b"), vm.state.value.selection)
        assertTrue(store.forgotten.isEmpty())
    }

    @Test fun aRefusedNameIsReported() = runBlocking {
        val vm = PeopleViewModel(Device { ack(false) }.send)
        val store = Store()
        vm.renameNow(store, "a", "Ana")
        assertTrue(store.renamed.isEmpty())
        assertEquals("The name couldn't be saved. Try again.", vm.state.value.toast?.text?.english())
    }
}
