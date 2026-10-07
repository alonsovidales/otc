// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import android.content.Context
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.WindowInsetsSides
import androidx.compose.foundation.layout.consumeWindowInsets
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.ime
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBars
import androidx.compose.foundation.layout.union
import androidx.compose.foundation.layout.only
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawing
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBars
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material.icons.filled.Notifications
import androidx.compose.material.icons.filled.PhotoLibrary
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material.icons.outlined.Forum
import androidx.compose.material3.Badge
import androidx.compose.material3.BadgedBox
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.layout.boundsInRoot
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.layout.positionInRoot
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.repeatOnLifecycle
import androidx.lifecycle.viewmodel.compose.viewModel
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.R
import cloud.offthe.otc.data.FaceRecognition
import cloud.offthe.otc.data.NotificationsModel
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.GetStatus
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.UpdateAlert
import cloud.offthe.otc.ui.files.FilesExplorerView
import cloud.offthe.otc.ui.files.FilesNav
import cloud.offthe.otc.ui.files.FilesRequest
import cloud.offthe.otc.ui.gallery.GalleryPage
import cloud.offthe.otc.ui.gallery.PeopleViewModel
import cloud.offthe.otc.ui.gallery.PhotoGalleryView
import cloud.offthe.otc.ui.gallery.PhotoGalleryViewModel
import cloud.offthe.otc.ui.gallery.TopSearchField
import cloud.offthe.otc.ui.gallery.TopSearchPanel
import cloud.offthe.otc.ui.gallery.TopSearchViewModel
import cloud.offthe.otc.ui.gallery.rememberSearchOptions
import cloud.offthe.otc.ui.settings.SettingsView
import cloud.offthe.otc.ui.settings.StatusViewModel
import cloud.offthe.otc.ui.social.SocialFeedView
import cloud.offthe.otc.ui.theme.Ember
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.delay
import kotlin.math.roundToInt

// Port of MainView.swift: the five tabs (Alerts, Social, Files, Images,
// Settings), a badge on Alerts, and the connection overlays (issue #56).
//
// On a window 600dp or more across (a phone turned sideways, the Fold
// unfolded, a tablet, a wide enough split screen) it is laid out as the
// web app is (App.tsx): a top bar with the menu button, the logo and the
// search, and the left menu (Sidebar.kt) with every section - People,
// Collections and Friends among them, as pages of their own - in place of
// the bottom bar. 1024dp and wider, the menu is whole or a rail of icons,
// switched with the menu button and remembered; 600-1023dp it is always
// the rail, and the button lays the whole menu over the page. Narrower,
// the bottom bar as it always was. Whichever the layout, the section is
// one (`section`), and the pages are drawn in the same place in the tree,
// so turning or folding the phone keeps the section and its state.
private data class Tab(val section: Section, val icon: ImageVector)

private val tabs = listOf(
    Tab(Section.Alerts, Icons.Default.Notifications),
    Tab(Section.Social, Icons.Outlined.Forum),
    Tab(Section.Files, Icons.Default.Folder),
    Tab(Section.Images, Icons.Default.PhotoLibrary),
    Tab(Section.Settings, Icons.Default.Settings),
)

/** A short window (a phone turned sideways): the rail tightens (Sidebar.css's max-height: 679px). */
private val SHORT_WINDOW = 680.dp

// The whole menu or the rail on a wide window, remembered as the files
// view mode is (the web's otc_menu_open).
private const val MENU_OPEN_KEY = "menu_open"
private fun uiPrefs() = OTCApp.instance.getSharedPreferences("otc_settings", Context.MODE_PRIVATE)
private fun loadMenuOpen(): Boolean = try { uiPrefs().getBoolean(MENU_OPEN_KEY, true) } catch (_: Exception) { true }
private fun saveMenuOpen(open: Boolean) { try { uiPrefs().edit().putBoolean(MENU_OPEN_KEY, open).apply() } catch (_: Exception) {} }

@Composable
fun MainView(secrets: SecretsStore) {
    var sectionId by rememberSaveable { mutableStateOf(Section.Social.id) }
    val section = Section.of(sectionId)
    // The search (TopSearch.kt), in the Images header or the wide top bar.
    val topSearch: TopSearchViewModel = viewModel(key = "topsearch")
    val focusManager = LocalFocusManager.current
    fun go(s: Section) {
        // Another section ends the search (what is typed stays); a change
        // of layout, which only moves its field, doesn't.
        if (s.id != sectionId && topSearch.open) { topSearch.close(); focusManager.clearFocus() }
        sectionId = s.id
    }
    // Signed out: a search left open doesn't come back at the next sign-in.
    DisposableEffect(Unit) { onDispose { topSearch.close() } }
    val unread by NotificationsModel.unacknowledgedCount.collectAsState()
    val deepLink by NotificationsModel.pendingDeepLink.collectAsState()
    val statusCode by OTCConnection.statusCode.collectAsState()
    val lastError by OTCConnection.lastError.collectAsState()
    val connectionFailed by OTCConnection.connectionFailed.collectAsState()
    val faces by FaceRecognition.enabled.collectAsState()

    // Issue #151: one failed attempt is not a connection problem - at
    // launch the first try often goes out before the network is up, and
    // the next one works. The card with the connection settings is shown
    // only once connecting has kept failing for a few seconds, retried
    // meanwhile.
    var showConnectionProblem by remember { mutableStateOf(false) }
    LaunchedEffect(connectionFailed) {
        if (!connectionFailed) { showConnectionProblem = false; return@LaunchedEffect }
        repeat(3) {
            kotlinx.coroutines.delay(2_500)
            try { OTCConnection.ensureConnected(); return@LaunchedEffect } catch (_: Exception) {}
        }
        showConnectionProblem = OTCConnection.connectionFailed.value
    }

    val alertsRequested by NotificationsModel.alertsRequested.collectAsState()
    val appContext = androidx.compose.ui.platform.LocalContext.current.applicationContext

    // A post opens in Social (which takes the link); friend requests in Friends.
    LaunchedEffect(deepLink) {
        when (deepLink) {
            is NotificationsModel.DeepLink.Post -> go(Section.Social)
            is NotificationsModel.DeepLink.FriendRequests -> { go(Section.Friends); NotificationsModel.consumeDeepLink() }
            null -> {}
        }
    }
    // Issue #125: a tapped push lands on Alerts.
    LaunchedEffect(alertsRequested) {
        if (alertsRequested) { go(Section.Alerts); NotificationsModel.consumeAlertsRequest() }
    }
    // Issue #125: signed in to a device - hand it this phone's push token.
    LaunchedEffect(Unit) { cloud.offthe.otc.push.FCMPush.register(appContext) }
    // Issue #183: a tapped update alert lands on Settings.
    val settingsRequested by NotificationsModel.settingsRequested.collectAsState()
    LaunchedEffect(settingsRequested) {
        if (settingsRequested) { go(Section.Settings); NotificationsModel.consumeSettingsRequest() }
    }
    // Whether face recognition is on, for People: asked once signed in
    // (Settings keeps it current after that). People gives way to Images
    // when it is off.
    LaunchedEffect(Unit) { FaceRecognition.watch() }
    LaunchedEffect(faces, section) { if (faces == false && section == Section.People) go(Section.Images) }
    // The search sent something to Files (a folder, a file in it, or the
    // documents searched for a word): Files shows and takes it.
    val filesRequest by FilesNav.pending.collectAsState()
    LaunchedEffect(filesRequest) {
        if (filesRequest is FilesRequest.Show || filesRequest is FilesRequest.Search) go(Section.Files)
    }

    // Issue #183: the device's status, at launch and every 5 minutes while
    // in the foreground, for its update alert. A failed fetch keeps the
    // last answer.
    var updateAlert by remember { mutableStateOf<UpdateAlert?>(null) }
    val lifecycle = androidx.compose.ui.platform.LocalLifecycleOwner.current.lifecycle
    LaunchedEffect(lifecycle) {
        lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            while (true) {
                try {
                    val resp = OTCConnection.request { it.setReqGetStatus(GetStatus.getDefaultInstance()) }
                    if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_STATUS) {
                        updateAlert = if (resp.respStatus.hasUpdateAlert()) resp.respStatus.updateAlert else null
                    }
                } catch (_: Exception) {}
                delay(5 * 60_000L)
            }
        }
    }
    val critical = updateAlert?.takeIf { it.level == "critical" }

    // The wide layout's menu: whole or a rail (by choice, 1024dp and wider),
    // the drawer over the page (600-1023dp), the storage details at its foot.
    var menuOpen by remember { mutableStateOf(loadMenuOpen()) }
    var drawerOpen by rememberSaveable { mutableStateOf(false) }
    var storageDetails by rememberSaveable { mutableStateOf(false) }
    // The rail's gauge asked for the storage: the whole menu opens scrolled to it.
    var revealStorage by remember { mutableStateOf(false) }
    // Social's composer, opened from the top bar's New post (the feed hands it over).
    var openComposer by remember { mutableStateOf<(() -> Unit)?>(null) }

    BoxWithConstraints(Modifier.fillMaxSize()) {
        val widthDp = maxWidth.value
        val layout = menuLayout(widthDp, menuOpen)
        val wide = layout != MenuLayout.NONE
        val canBeWhole = widthDp >= MENU_FULL_MIN_DP
        val shortWindow = maxHeight < SHORT_WINDOW
        // A drawer left open while the window grew (or narrowed past the
        // menu) would otherwise come back on its own.
        LaunchedEffect(canBeWhole, wide) { if (canBeWhole || !wide) drawerOpen = false }

        // What the wide layout's top bar and menu need: the search (on the
        // gallery's tags and people, as in Images), and the storage.
        val gallery: PhotoGalleryViewModel? = if (wide) viewModel(key = "gallery") { PhotoGalleryViewModel(secrets.deviceId.value) } else null
        val search: TopSearchViewModel? = if (wide) topSearch else null
        val people: PeopleViewModel? = if (wide) viewModel(key = "people") else null
        LaunchedEffect(gallery) { gallery?.loadListsOnce() }
        val searchOpen = search?.open == true
        LaunchedEffect(searchOpen) { if (searchOpen) drawerOpen = false }
        val menuStatus: StatusViewModel = viewModel(key = "menuStatus")
        val statusState by menuStatus.state.collectAsState()
        // The storage is read every 30 s while the menu shows, every 5 s
        // while its details (CPU, memory) are open, as the web's 5 s - and
        // until a first answer comes, so a failed first ask (the connection
        // still coming up) doesn't leave the gauge empty for 30 s.
        val detailsShown = storageDetails && (layout == MenuLayout.FULL || drawerOpen)
        val haveStatus = statusState.status != null
        LaunchedEffect(wide, detailsShown, haveStatus, lifecycle) {
            if (!wide) { menuStatus.stop(); return@LaunchedEffect }
            lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
                menuStatus.start(if (detailsShown || !haveStatus) 5_000L else 30_000L)
                try { awaitCancellation() } finally { menuStatus.stop() }
            }
        }
        val storage = remember(statusState) { summarizeStorage(statusState.status, statusState.errorText) }
        val gst = gallery?.state?.collectAsState()?.value
        val current = menuCurrent(section, gst?.activeGroup != null)

        fun pick(s: Section) {
            when (s) {
                // Images in the menu is the whole library, as on the web:
                // whatever was searched for is left.
                Section.Images -> gallery?.showAll()
                // Files in the menu is the folder, not a search's results.
                Section.Files -> FilesNav.leaveFilesSearch()
                // A new visit, with nothing picked from the last one.
                Section.People -> if (section != Section.People) people?.enter()
                else -> {}
            }
            go(s)
        }
        // The field's place in the window, for the panel hanging from it,
        // and where the page starts (under the top bar), in the window too.
        var fieldBounds by remember { mutableStateOf(Rect.Zero) }
        var contentTop by remember { mutableStateOf(0f) }

        Scaffold(topBar = {
            // Nothing at all when there is nothing to show: an empty bar
            // would take the place of the status bar's inset in the page's
            // padding, and the page would go under the status bar.
            if (critical != null || wide) Column {
                if (critical != null) CriticalUpdateBanner(critical) { go(Section.Settings) }
                if (wide && gallery != null && search != null && gst != null) {
                    val options = rememberSearchOptions(search, gst)
                    WideTopBar(
                        statusInset = critical == null, wholeMenuRoom = canBeWhole,
                        menuExpanded = if (canBeWhole) menuOpen else drawerOpen,
                        onMenu = {
                            if (canBeWhole) { menuOpen = !menuOpen; saveMenuOpen(menuOpen) } else drawerOpen = !drawerOpen
                        },
                        newPost = openComposer.takeIf { section == Section.Social }, iconOnlyAction = widthDp < 700,
                    ) {
                        TopSearchField(
                            search, gallery, gst, options,
                            Modifier.widthIn(max = 720.dp).fillMaxWidth().onGloballyPositioned { fieldBounds = it.boundsInRoot() },
                            onShowPhotos = { if (section != Section.Images) go(Section.Images) },
                        )
                    }
                }
            }
        }, bottomBar = {
            if (!wide) NavigationBar {
                tabs.forEach { t ->
                    NavigationBarItem(
                        selected = section.tab == t.section,
                        onClick = {
                            // Files picked in the tab bar: whatever search
                            // results show, back to the folder (as the web's menu).
                            if (t.section == Section.Files) FilesNav.leaveFilesSearch()
                            // Another tab, or the tab already showing from
                            // one of its pages (Images' People page): the
                            // tab's own root, as Android's tabs do. The root
                            // itself, tapped again, stays as it is.
                            if (section != t.section) go(t.section)
                        },
                        icon = {
                            if (t.section == Section.Alerts && unread > 0) {
                                BadgedBox(badge = { Badge { Text(unread.toString()) } }) { Icon(t.icon, t.section.label) }
                            } else Icon(t.icon, t.section.label)
                        },
                        label = { Text(t.section.label) },
                    )
                }
            }
        }) { pad ->
            Box(Modifier.fillMaxSize().padding(pad).consumeWindowInsets(pad).onGloballyPositioned { contentTop = it.positionInRoot().y }) {
                // A cutout or a side navigation bar (a phone turned sideways)
                // is kept clear of; the menu takes the room at the left.
                Row(Modifier.fillMaxSize().windowInsetsPadding(WindowInsets.safeDrawing.only(WindowInsetsSides.Horizontal))) {
                    if (wide) Sidebar(
                        layout, current, faces = faces == true, unread = unread, storage = storage,
                        details = storageDetails, onToggleDetails = { storageDetails = !storageDetails },
                        onPick = ::pick,
                        onStorage = {
                            storageDetails = true
                            revealStorage = true
                            if (canBeWhole) { menuOpen = true; saveMenuOpen(true) } else drawerOpen = true
                        },
                        compact = shortWindow,
                        revealStorage = revealStorage, onStorageRevealed = { revealStorage = false },
                    )
                    // The window is edge to edge, where adjustResize no longer
                    // lifts anything above the keyboard: imePadding does, for
                    // every section (the feed's comment field sat under the
                    // keyboard, invisible).
                    Box(Modifier.weight(1f).fillMaxHeight().imePadding()) {
                        // Only the open section is drawn; the bottom bar's tab
                        // draws People, Collections and Friends on a narrow
                        // window, and the wide layout draws them from the
                        // same place, so a change of layout keeps them.
                        when (section.tab) {
                            Section.Alerts -> NotificationsListView()
                            Section.Social -> SocialFeedView(
                                wide = wide, friendsOpen = section == Section.Friends,
                                onFriendsOpen = { go(if (it) Section.Friends else Section.Social) },
                                onRegisterOpenComposer = { openComposer = it },
                            )
                            Section.Files -> FilesExplorerView(initialPath = "/")
                            Section.Images -> PhotoGalleryView(
                                deviceId = secrets.deviceId.value, wide = wide,
                                page = when (section) {
                                    Section.People -> GalleryPage.PEOPLE
                                    Section.Collections -> GalleryPage.COLLECTIONS
                                    else -> GalleryPage.PHOTOS
                                },
                                onPage = { p ->
                                    go(when (p) {
                                        GalleryPage.PEOPLE -> Section.People
                                        GalleryPage.COLLECTIONS -> Section.Collections
                                        GalleryPage.PHOTOS -> Section.Images
                                    })
                                },
                            )
                            Section.Settings -> SettingsView(secrets = secrets)
                            else -> {}
                        }
                    }
                }
                if (wide && layout == MenuLayout.RAIL && !canBeWhole) {
                    MenuDrawer(
                        open = drawerOpen, onClose = { drawerOpen = false }, current = current, faces = faces == true, unread = unread,
                        storage = storage, details = storageDetails, onToggleDetails = { storageDetails = !storageDetails }, onPick = ::pick,
                        revealStorage = revealStorage, onStorageRevealed = { revealStorage = false },
                    )
                }
                val code = statusCode
                val context = androidx.compose.ui.platform.LocalContext.current
                Box(Modifier.fillMaxSize().imePadding()) {
                    if (code != null) {
                        DeviceUnreachableView(message = lastError ?: "", code = code, onClose = { logOut(context, secrets, unregisterPush = false, keepDevice = true) })
                    } else if (showConnectionProblem) {
                        ConnectionProblemView(secrets = secrets, onLeave = { e, p -> logOut(context, secrets, unregisterPush = false, keepDevice = true, lastDevice = e to p) })
                    }
                }
            }
        }
        // The search's panel, hanging from the top bar's field over the
        // menu and the page. It is laid over the whole Scaffold, not in its
        // page, so that on a short window it can rise over the top bar
        // (TopSearchPanel's riseTo); the taps that end the search are still
        // caught under the top bar only, so the field and the menu button
        // keep theirs. The device's unreachable and connection cards go
        // over it as before: no panel while one of them shows.
        if (wide && gallery != null && search != null && gst != null && statusCode == null && !showConnectionProblem) {
            var origin by remember { mutableStateOf(Offset.Zero) }
            val density = LocalDensity.current
            // As high as the panel may rise: just under the status bar.
            val ceiling = WindowInsets.statusBars.getTop(density) + with(density) { 4.dp.roundToPx() }
            Box(
                Modifier.fillMaxSize().padding(top = with(density) { contentTop.toDp() })
                    .onGloballyPositioned { origin = it.positionInRoot() }
                    // Above the keyboard, or the navigation bar without it.
                    .windowInsetsPadding(WindowInsets.ime.union(WindowInsets.navigationBars).only(WindowInsetsSides.Bottom)),
            ) {
                val options = rememberSearchOptions(search, gst)
                TopSearchPanel(
                    search, gallery, gst, options,
                    onShowPhotos = { if (section != Section.Images) go(Section.Images) },
                    dropdown = true,
                    dropdownOffset = IntOffset(
                        (fieldBounds.left - origin.x).roundToInt(),
                        (fieldBounds.bottom - origin.y + with(density) { 6.dp.toPx() }).roundToInt(),
                    ),
                    dropdownWidth = with(density) { fieldBounds.width.toDp() },
                    riseTo = (ceiling - origin.y).roundToInt(),
                )
            }
        }
    }
}

/**
 * The wide layout's top bar (the web's .topbar): the menu button - lined up
 * with the rail's icons under it - the logo, the search ([search]: as
 * wide as there is room for, 720dp at most) and the page's own action
 * (Social's New post: just its "+" under 700dp). [wholeMenuRoom]: the
 * logo takes the whole menu's width less the button's, so the search
 * starts where the page does.
 */
@Composable
private fun WideTopBar(
    statusInset: Boolean, wholeMenuRoom: Boolean, menuExpanded: Boolean, onMenu: () -> Unit,
    newPost: (() -> Unit)?, iconOnlyAction: Boolean, search: @Composable () -> Unit,
) {
    val colors = menuColors()
    Column(Modifier.fillMaxWidth().background(colors.surface)) {
        Row(
            Modifier.fillMaxWidth()
                .let { if (statusInset) it.windowInsetsPadding(WindowInsets.statusBars) else it }
                .windowInsetsPadding(WindowInsets.safeDrawing.only(WindowInsetsSides.Horizontal))
                .height(64.dp).padding(start = 18.dp, end = 16.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            MenuButton(menuExpanded, onMenu)
            Box(if (wholeMenuRoom) Modifier.width(SIDEBAR_WIDTH - 60.dp) else Modifier) {
                Image(
                    painterResource(R.drawable.otc_logo), "Off The Cloud",
                    Modifier.padding(start = 4.dp).height(38.dp), contentScale = ContentScale.Fit,
                )
            }
            Box(Modifier.weight(1f)) { search() }
            if (newPost != null) {
                OutlinedButton(
                    onClick = newPost, border = BorderStroke(1.dp, Ember), shape = CircleShape,
                    colors = ButtonDefaults.outlinedButtonColors(contentColor = Ember),
                    contentPadding = PaddingValues(horizontal = if (iconOnlyAction) 12.dp else 16.dp),
                    modifier = Modifier.height(if (iconOnlyAction) 40.dp else 38.dp).semantics { contentDescription = "New post" },
                ) {
                    Text("+", fontSize = 18.sp, fontWeight = FontWeight.SemiBold)
                    if (!iconOnlyAction) Text(" New post", fontWeight = FontWeight.SemiBold)
                }
            }
        }
        Hairline()
    }
}

/** Issue #183: stays up while the device has a critical update not installed. */
@Composable
private fun CriticalUpdateBanner(alert: UpdateAlert, onUpdate: () -> Unit) {
    Row(
        Modifier.fillMaxWidth().background(Color(0xFFE53935)).statusBarsPadding().padding(start = 16.dp, end = 8.dp, top = 8.dp, bottom = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Icon(Icons.Default.Warning, null, tint = Color.White)
        Spacer(Modifier.width(12.dp))
        Text(
            "Critical update ${alert.version} available. ${alert.summary.trim().let { if (it.isEmpty()) "" else "$it " }}Install it as soon as possible.",
            color = Color.White, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.weight(1f),
        )
        TextButton(onClick = onUpdate) { Text("Update", color = Color.White, fontWeight = FontWeight.Bold) }
    }
}
