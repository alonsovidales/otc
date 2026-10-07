// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import kotlinx.coroutines.runBlocking
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
        assertEquals("Merged 3 into Ana", st.toast?.text)
    }

    @Test fun mergeKeepingANamedFaceSendsNoRename() = runBlocking {
        val dev = Device { ack(true) }
        val vm = PeopleViewModel(dev.send)
        picked(vm, "a", "b")
        assertTrue(vm.mergeNow(Store(), p("a", "Leo", 5), "Leo"))
        assertEquals(listOf(ReqEnvelope.PayloadCase.REQ_MERGE_PEOPLE), dev.sent.map { it.payloadCase })
        assertEquals("Merged 2 into Leo", vm.state.value.toast?.text)
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
        assertEquals("The name couldn't be saved, so nothing was merged. Try again.", st.confirmError)
        assertEquals(PeopleViewModel.Confirm.Merge, st.confirm)
        assertEquals(listOf("a", "b"), st.selection)
        assertTrue(store.forgotten.isEmpty())
    }

    @Test fun anUnansweredMergeSaysSo() = runBlocking {
        val vm = PeopleViewModel(Device { null }.send)
        picked(vm, "a", "b")
        assertFalse(vm.mergeNow(Store(), p("a", "Leo", 5), "Leo"))
        assertEquals("Your device didn't answer. Try again.", vm.state.value.confirmError)
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
        assertEquals("1 of 4 people couldn't be deleted and is still selected. Try again.", st.toast?.text)
        assertTrue(st.toast!!.error)
    }

    @Test fun nothingDeletedKeepsTheQuestionOpen() = runBlocking {
        val vm = PeopleViewModel(Device { null }.send)
        val store = Store()
        picked(vm, "a", "b")
        vm.askDelete(listOf("a", "b"))
        assertFalse(vm.removeNow(store, listOf("a", "b")))
        val st = vm.state.value
        assertEquals("Your device didn't answer. Try again.", st.confirmError)
        assertEquals(PeopleViewModel.Confirm.Delete(listOf("a", "b")), st.confirm)
        assertEquals(listOf("a", "b"), st.selection)
        assertTrue(store.forgotten.isEmpty())
    }

    @Test fun oneDeletedSaysSo() = runBlocking {
        val vm = PeopleViewModel(Device { ack(true) }.send)
        vm.askDelete(listOf("a"))
        assertTrue(vm.removeNow(Store(), listOf("a")))
        assertEquals("Person deleted", vm.state.value.toast?.text)
    }

    @Test fun aRefusedNameIsReported() = runBlocking {
        val vm = PeopleViewModel(Device { ack(false) }.send)
        val store = Store()
        vm.renameNow(store, "a", "Ana")
        assertTrue(store.renamed.isEmpty())
        assertEquals("The name couldn't be saved. Try again.", vm.state.value.toast?.text)
    }
}
