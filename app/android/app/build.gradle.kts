plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
}

// Issue #125: google-services.json (the Firebase project's client config)
// is kept out of git - copy it into app/ before building. Without it the
// app still builds, just with no push notifications (FCMPush checks).
if (file("google-services.json").exists()) {
    apply(plugin = "com.google.gms.google-services")
} else {
    logger.warn("app/google-services.json is missing: building without push notifications")
}

// Issue #129: release signing. The upload keystore lives outside the repo
// (~/.otc/otc-upload.jks unless OTC_UPLOAD_KEYSTORE says otherwise) and its
// password comes from the environment - `make android-release` reads it
// from the macOS Keychain (service otc-android-upload) for the one build.
// Without them a release build is left unsigned; -Potc.signWithDebug signs
// it with the debug key instead, to try a minified build on a phone over
// an installed debug build.
val uploadKeystore = file(System.getenv("OTC_UPLOAD_KEYSTORE") ?: "${System.getProperty("user.home")}/.otc/otc-upload.jks")
val uploadPassword: String? = System.getenv("OTC_UPLOAD_PASSWORD")

// Play needs every upload's versionCode to be higher than the last: the
// commit count only ever grows on main.
fun gitCommitCount(): Int = try {
    val p = ProcessBuilder("git", "rev-list", "--count", "HEAD").directory(rootDir).start()
    p.inputStream.bufferedReader().readText().trim().toInt()
} catch (e: Exception) { 1 }

android {
    namespace = "cloud.offthe.otc"
    compileSdk = 36

    defaultConfig {
        // Same product identity as the iOS/macOS apps (cloud.off-the.OffTheCloud
        // there); Android package names can't contain a hyphen.
        applicationId = "cloud.offthe.otc"
        minSdk = 29
        targetSdk = 36
        versionCode = gitCommitCount()
        versionName = "1.0"
    }

    signingConfigs {
        create("upload") {
            if (uploadKeystore.exists() && uploadPassword != null) {
                storeFile = uploadKeystore
                storePassword = uploadPassword
                keyAlias = "otc-upload"
                keyPassword = uploadPassword
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
            signingConfig = when {
                project.hasProperty("otc.signWithDebug") -> signingConfigs.getByName("debug")
                uploadKeystore.exists() && uploadPassword != null -> signingConfigs.getByName("upload")
                else -> null
            }
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
    buildFeatures { compose = true }
    sourceSets {
        // `make pb` writes the protobuf-generated Kotlin/Java here, next to
        // the Swift output for iOS - see the pb target in the root makefile.
        getByName("main").java.srcDirs("src/main/proto-gen")
    }
}

dependencies {
    val composeBom = platform("androidx.compose:compose-bom:2025.09.00")
    implementation(composeBom)
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.ui:ui-tooling-preview")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.material:material-icons-extended")
    implementation("androidx.activity:activity-compose:1.11.0")
    implementation("androidx.navigation:navigation-compose:2.9.4")
    implementation("androidx.lifecycle:lifecycle-viewmodel-compose:2.9.3")
    implementation("androidx.lifecycle:lifecycle-runtime-compose:2.9.3")
    implementation("androidx.core:core-ktx:1.17.0")
    // Something transitive still brings Fragment 1.0, which breaks the
    // ActivityResult APIs (release lint: InvalidFragmentVersionForActivityResult).
    implementation("androidx.fragment:fragment-ktx:1.8.9")
    // Issue #137: "Continue with Apple/Google" in the Bluetooth setup opens a Custom Tab.
    implementation("androidx.browser:browser:1.9.0")
    // The same protobuf envelope protocol as every other client; lite
    // runtime for the generated code (protoc --java_out / --kotlin_out).
    implementation("com.google.protobuf:protobuf-kotlin-lite:4.36.2")
    // WebSocket client (OkHttp), the Android counterpart of URLSessionWebSocketTask.
    implementation("com.squareup.okhttp3:okhttp:5.1.0")
    // Encrypted storage for the device secrets (the Keychain's counterpart).
    implementation("androidx.security:security-crypto:1.1.0")
    // Background photo sync (the BGTaskScheduler counterpart).
    implementation("androidx.work:work-runtime-ktx:2.10.5")
    // Images/video in the feed and gallery.
    implementation("io.coil-kt.coil3:coil-compose:3.3.0")
    implementation("androidx.media3:media3-exoplayer:1.8.0")
    implementation("androidx.media3:media3-ui:1.8.0")
    // Issue #190: video from the device itself at home, over OkHttp pinned to its certificate.
    implementation("androidx.media3:media3-datasource-okhttp:1.8.0")
    // Issue #127: the EXIF panel's map - OpenStreetMap through osmdroid, no API key.
    implementation("org.osmdroid:osmdroid-android:6.1.20")
    implementation("androidx.media3:media3-ui-compose:1.8.0")
    // Issue #125: push notifications through Firebase Cloud Messaging.
    implementation(platform("com.google.firebase:firebase-bom:34.3.0"))
    implementation("com.google.firebase:firebase-messaging")
    debugImplementation("androidx.compose.ui:ui-tooling")
    testImplementation("junit:junit:4.13.2")
}
