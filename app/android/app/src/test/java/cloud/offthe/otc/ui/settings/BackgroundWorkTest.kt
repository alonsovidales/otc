// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.BackgroundTask
import cloud.offthe.otc.proto.Status
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// Settings > Background work (Status.background_tasks, release 152): the
// rows from the device's Status, shown only while there are some.
class BackgroundWorkTest {
    private fun task(kind: String, text: String, done: Long, total: Long) =
        BackgroundTask.newBuilder().setKind(kind).setText(text).setDone(done).setTotal(total).build()

    private fun status(vararg tasks: BackgroundTask) = Status.newBuilder().addAllBackgroundTasks(tasks.toList()).build()

    @Test fun nothingWithoutWork() {
        // No answer yet, an older device (no field), nothing to do: no section.
        assertTrue(BackgroundWork.tasks(null).isEmpty())
        assertTrue(BackgroundWork.tasks(Status.getDefaultInstance()).isEmpty())
        assertTrue(BackgroundWork.tasks(status()).isEmpty())
    }

    @Test fun oneRowPerTaskInTheDevicesOrderAndWords() {
        val rows = BackgroundWork.tasks(status(
            task("smart_search", " Getting to know your photos ", 4_210, 12_800),
            task("thumbnails", "Making the small pictures", 950, 1_200),
        ))
        assertEquals(listOf("smart_search", "thumbnails"), rows.map { it.kind })
        assertEquals("Getting to know your photos", rows[0].text)
        assertFalse(rows[0].indeterminate)
        assertEquals(4_210f / 12_800f, rows[0].progress, 0.0001f)
        // The numbers as the app's other counts.
        assertEquals("4,210 of 12,800", rows[0].count()!!.english())
        assertEquals("950 of 1,200", rows[1].count()!!.english())
    }

    @Test fun anUnknownTotalIsAnIndeterminateBarWithNoCount() {
        val row = BackgroundWork.tasks(status(task("places", "Finding where your photos were taken", 12, 0))).single()
        assertTrue(row.indeterminate)
        assertEquals(0f, row.progress, 0f)
        assertNull(row.count())
    }

    @Test fun oddNumbersAreKeptInBounds() {
        val rows = BackgroundWork.tasks(status(
            task("a", "Past the end", 130, 100),
            task("b", "Below zero", -5, 10),
            task("c", "No total", -3, -1),
        ))
        assertEquals(100L, rows[0].done)
        assertEquals(1f, rows[0].progress, 0f)
        assertEquals("100 of 100", rows[0].count()!!.english())
        assertEquals(0L, rows[1].done)
        assertTrue(rows[2].indeterminate)
        assertEquals(0L, rows[2].done)
        assertEquals(0L, rows[2].total)
    }

    @Test fun rowsAreKeyedByKind() {
        // A kind given twice shows once (a key is the row's identity), and
        // a missing kind still gets a key of its own.
        val rows = BackgroundWork.tasks(status(
            task("dates", "Reading photo dates", 1, 4),
            task("dates", "Reading photo dates again", 2, 4),
            task("", "Something new", 0, 0),
            task("", "Something else", 0, 0),
        ))
        assertEquals(listOf("Reading photo dates", "Something new", "Something else"), rows.map { it.text })
        assertEquals(rows.size, rows.map { it.kind }.toSet().size)
    }
}
