// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.StrokeJoin
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.graphics.vector.PathParser
import androidx.compose.ui.graphics.vector.group
import androidx.compose.ui.unit.dp

// The web menu's icons (web/src/components/NavIcons.tsx), drawn from the
// same path data: 24-unit outlines, stroked with round caps and joins,
// tinted by Icon like any Material icon. The web strokes them at 1.6 and
// shows them at 22px; next to Material's icons at 24dp they read light at
// that weight, so here they are stroked at 1.8 (NAV_STROKE). Change a
// shape on the web and here (and in iOS's NavIcons) together.
//
// All in use: the wide layout's menu and top bar (Sidebar.kt, MainView),
// Images' People and Collections buttons, and the People and Collections pages.

/** The stroke the menu's icons are drawn with, in 24ths of their size. */
const val NAV_STROKE = 1.8f

/** SVG <circle> as path data. */
internal fun circlePath(cx: Float, cy: Float, r: Float) =
    "M${cx - r},${cy}a$r,$r 0 1,0 ${2 * r},0a$r,$r 0 1,0 ${-2 * r},0Z"

/** SVG <rect> with rounded corners as path data. */
internal fun rectPath(x: Float, y: Float, w: Float, h: Float, rx: Float): String {
    val iw = w - 2 * rx
    val ih = h - 2 * rx
    return "M${x + rx},${y}h${iw}a$rx,$rx 0 0 1 $rx,${rx}v${ih}a$rx,$rx 0 0 1 ${-rx},${rx}" +
        "h${-iw}a$rx,$rx 0 0 1 ${-rx},${-rx}v${-ih}a$rx,$rx 0 0 1 $rx,${-rx}Z"
}

/**
 * An outline icon from SVG path data in a 24x24 box, as NavIcons.tsx's
 * Svg draws them; [scale] and [offset] are a transform="translate(offset
 * offset) scale(scale)" around all of it (the stroke scales with it, as in
 * SVG).
 */
internal fun outlineIcon(name: String, vararg paths: String, stroke: Float = NAV_STROKE, scale: Float = 1f, offset: Float = 0f): ImageVector =
    ImageVector.Builder(name = name, defaultWidth = 24.dp, defaultHeight = 24.dp, viewportWidth = 24f, viewportHeight = 24f).apply {
        group(scaleX = scale, scaleY = scale, translationX = offset, translationY = offset) {
            for (d in paths) addPath(
                pathData = PathParser().parsePathString(d).toNodes(),
                fill = null,
                stroke = SolidColor(Color.Black),
                strokeLineWidth = stroke,
                strokeLineCap = StrokeCap.Round,
                strokeLineJoin = StrokeJoin.Round,
            )
        }
    }.build()

object NavIcons {
    val Menu: ImageVector by lazy { outlineIcon("Menu", "M4 7h16M4 12h16M4 17h16") }

    val Images: ImageVector by lazy {
        outlineIcon("Images", rectPath(3.5f, 4.5f, 17f, 15f, 2.5f), circlePath(9f, 10f, 1.6f), "m4 17 4.5-4.5 3.5 3.5 2.5-2.5L20 18")
    }

    /** A face in a viewfinder: the faces found in the photos (Friends is the two people). */
    val People: ImageVector by lazy {
        outlineIcon(
            "People",
            "M4 8V6a2 2 0 0 1 2-2h2M16 4h2a2 2 0 0 1 2 2v2M20 16v2a2 2 0 0 1-2 2h-2M8 20H6a2 2 0 0 1-2-2v-2",
            circlePath(12f, 10.5f, 2.8f),
            "M7.5 17.5c.8-2.2 2.5-3.3 4.5-3.3s3.7 1.1 4.5 3.3",
        )
    }

    /** A photo with another behind it: photos put together. */
    val Collections: ImageVector by lazy {
        outlineIcon(
            "Collections",
            "M7.5 4.5h10a3 3 0 0 1 3 3v10",
            rectPath(3.5f, 7.5f, 13f, 13f, 2f),
            circlePath(8f, 11.5f, 1.3f),
            "m4 19 4-4 3 3 2-2 3.5 3.5",
        )
    }

    /** Images' "Add to collection" (PhotoGallery.tsx's AddToCollectionIcon): the same stack with a plus. */
    val AddToCollection: ImageVector by lazy {
        outlineIcon("AddToCollection", "M7.5 4.5h10a3 3 0 0 1 3 3v10", rectPath(3.5f, 7.5f, 13f, 13f, 2f), "M10 11v6M7 14h6")
    }

    val Files: ImageVector by lazy {
        outlineIcon("Files", "M3.5 7.5a2 2 0 0 1 2-2h4l2 2h7a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2v-10Z")
    }

    val Social: ImageVector by lazy {
        outlineIcon(
            "Social",
            "M4 5.5h12a1.5 1.5 0 0 1 1.5 1.5v7A1.5 1.5 0 0 1 16 15.5H9l-4 3.5v-3.5H4A1.5 1.5 0 0 1 2.5 14V7A1.5 1.5 0 0 1 4 5.5Z",
            "M20.5 9.5v7.5a1.5 1.5 0 0 1-1.5 1.5h-.5V21l-3.5-2.5",
        )
    }

    val Friends: ImageVector by lazy {
        outlineIcon(
            "Friends",
            circlePath(9f, 8f, 3f), "M3.5 19c0-3 2.5-5 5.5-5s5.5 2 5.5 5",
            circlePath(17f, 9f, 2.5f), "M15.5 14.2c2.4.3 4 2 4 4.8",
        )
    }

    val Alerts: ImageVector by lazy {
        outlineIcon(
            "Alerts",
            "M12 3a5 5 0 0 0-5 5v2.7c0 1.15-.45 2.25-1.26 3.06L4.5 15h15l-1.24-1.24A4.33 4.33 0 0 1 17 10.7V8a5 5 0 0 0-5-5Z",
            "M9.5 18a2.5 2.5 0 0 0 5 0",
        )
    }

    /**
     * The gear's outline fills the whole box: drawn at 85% so it weighs the
     * same as the others. Its line shrinks with it, as the web's <g
     * transform> does: Compose draws a group's paths inside the group's
     * transform, so the 1.8 here is 1.53 in the box (iOS's strokeScale).
     */
    val Settings: ImageVector by lazy {
        outlineIcon(
            "Settings",
            circlePath(12f, 12f, 3f),
            "M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09a1.65 1.65 0 0 0-1.08-1.51 1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09a1.65 1.65 0 0 0 1.51-1.08 1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1Z",
            scale = 0.85f, offset = 1.8f,
        )
    }

    val Storage: ImageVector by lazy {
        outlineIcon("Storage", rectPath(3.5f, 4.5f, 17f, 6f, 1.5f), rectPath(3.5f, 13.5f, 17f, 6f, 1.5f), "M7 7.5h.01M7 16.5h.01")
    }

    /** Points down: a section that opens below its row. */
    val Chevron: ImageVector by lazy { outlineIcon("Chevron", "m7 10 5 5 5-5") }
}
