// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.data

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update

/**
 * Issue #192: a folder was kept out of Images, or shown there again, from
 * Files. What Images holds changes with it - its photos, the tags found in
 * them, the people (an unnamed one left with no face goes) and the
 * collections' counts and covers - so the gallery (PhotoGalleryViewModel)
 * asks for all of them again when [generation] moves. The iOS app posts
 * `.otcImagesChanged` for the same.
 */
object ImagesChanged {
    private val _generation = MutableStateFlow(0)
    val generation: StateFlow<Int> = _generation

    fun bump() = _generation.update { it + 1 }
}
