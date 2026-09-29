plugins {
    id("com.android.application") version "8.13.0" apply false
    id("org.jetbrains.kotlin.android") version "2.2.20" apply false
    id("org.jetbrains.kotlin.plugin.compose") version "2.2.20" apply false
    // Issue #125: push through Firebase Cloud Messaging.
    id("com.google.gms.google-services") version "4.4.3" apply false
}
