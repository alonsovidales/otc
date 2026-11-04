// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.media3.common.Player

/**
 * [player] pauses when the app leaves the screen (Home, another app, the
 * phone locked: the Activity stops while the composition, and a viewer's
 * dialog, stay), as iOS pauses its player in the background: a video went
 * on playing out loud from the pocket. Coming back leaves it paused, to be
 * played again with its own controls.
 */
@Composable
fun PauseWhenStopped(player: Player?) {
    val owner = LocalLifecycleOwner.current
    DisposableEffect(owner, player) {
        if (player == null) return@DisposableEffect onDispose {}
        val observer = LifecycleEventObserver { _, event -> if (event == Lifecycle.Event.ON_STOP) player.pause() }
        owner.lifecycle.addObserver(observer)
        onDispose { owner.lifecycle.removeObserver(observer) }
    }
}
