plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
}

android {
    namespace = "cloud.offthe.otc"
    compileSdk = 36

    defaultConfig {
        // Same product identity as the iOS/macOS apps (cloud.off-the.OffTheCloud
        // there); Android package names can't contain a hyphen.
        applicationId = "cloud.offthe.otc"
        minSdk = 29
        targetSdk = 36
        versionCode = 1
        versionName = "1.0"
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
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
    implementation("androidx.media3:media3-ui-compose:1.8.0")
    debugImplementation("androidx.compose.ui:ui-tooling")
    testImplementation("junit:junit:4.13.2")
}
